//go:build awsderive

// Command derive establishes empirically which IAM actions a resource type
// needs, by granting a scratch role exactly the mapped actions and running real
// Terraform against a fixture.
//
// This creates real, billable AWS resources. It is a maintainer tool and is
// deliberately not part of the preflight binary users run in CI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/Caleb-Kelly-25/preflight/internal/derive"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

const confirmValue = "creates-real-resources"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("derive", flag.ContinueOnError)
	var (
		resourceType = fs.String("type", "", "Terraform resource type to derive, e.g. aws_vpc (required)")
		operation    = fs.String("operation", "create", "operation to derive: create, update or delete")
		fixtureDir   = fs.String("fixture", "", "directory holding the Terraform fixture (required)")
		region       = fs.String("region", "us-east-1", "AWS region")
		budget       = fs.Duration("budget", 4*time.Minute, "per-apply budget; exceeding it counts as a stall")
		maxAttempts  = fs.Int("max-attempts", 15, "cap on discovery attempts")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *resourceType == "" || *fixtureDir == "" {
		fs.Usage()
		return fmt.Errorf("--type and --fixture are required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(*region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	account, err := guard(ctx, cfg)
	if err != nil {
		return err
	}

	seed, err := seedActions(*resourceType, mapping.Operation(*operation))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "deriving %s %s in account %s\n", *resourceType, *operation, account)
	fmt.Fprintf(stdout, "seed: %d actions from the mapping database\n\n", len(seed))

	roleName := fmt.Sprintf("preflight-derive-%d", time.Now().Unix())
	roleARN, err := createScratchRole(ctx, cfg, roleName, account)
	if err != nil {
		return fmt.Errorf("creating scratch role: %w", err)
	}

	grantor := derive.NewAWSGrantor(cfg, roleName, roleARN)
	applier := &derive.TerraformApplier{
		Dir:    *fixtureDir,
		Region: *region,
		Creds:  func() derive.Credentials { return grantor.Latest },
	}

	// Teardown runs on every exit path, including Ctrl-C, on a context that is
	// not the cancelled one.
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := applier.Destroy(tctx); err != nil {
			fmt.Fprintf(stderr, "WARNING: destroy failed, resources may survive: %v\n", err)
		}
		if err := grantor.Revoke(tctx); err != nil {
			fmt.Fprintf(stderr, "WARNING: could not remove scratch role %s: %v\n", roleName, err)
		} else {
			fmt.Fprintf(stdout, "\nscratch role %s removed\n", roleName)
		}
	}()

	if err := applier.Init(ctx); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}

	d := &derive.Deriver{
		Grantor:       grantor,
		Applier:       applier,
		MaxAttempts:   *maxAttempts,
		AttemptBudget: *budget,
		Log:           stdout,
	}
	res, err := d.Derive(ctx, seed)
	if err != nil {
		return err
	}
	report(stdout, *resourceType, *operation, res)
	return nil
}

// guard enforces the three checks. The account check compares against a LIVE
// GetCallerIdentity rather than trusting the variable to be set, because that
// is what stops a run against production.
func guard(ctx context.Context, cfg aws.Config) (string, error) {
	declared := os.Getenv("PREFLIGHT_DERIVE_ACCOUNT")
	if declared == "" {
		return "", fmt.Errorf("set PREFLIGHT_DERIVE_ACCOUNT to the scratch account id")
	}
	if os.Getenv("PREFLIGHT_DERIVE_CONFIRM") != confirmValue {
		return "", fmt.Errorf("set PREFLIGHT_DERIVE_CONFIRM=%s; this creates real AWS resources", confirmValue)
	}
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("resolving caller identity: %w", err)
	}
	actual := aws.ToString(id.Account)
	if actual != declared {
		return "", fmt.Errorf(
			"refusing to run: credentials are for account %s but PREFLIGHT_DERIVE_ACCOUNT is %s",
			actual, declared)
	}
	return actual, nil
}

// seedActions reads what the mapping currently claims, including the read set,
// which is what the derivation starts from.
func seedActions(resourceType string, op mapping.Operation) ([]string, error) {
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		return nil, fmt.Errorf("loading mapping database: %w", err)
	}
	res, ok := db.Lookup(resourceType)
	if !ok {
		return nil, fmt.Errorf("%s is not in the mapping database", resourceType)
	}
	var out []string
	for _, a := range res.Operations[op] {
		out = append(out, a.Action)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no %s operation mapped", resourceType, op)
	}
	return append(out, res.ReadActions...), nil
}

func createScratchRole(ctx context.Context, cfg aws.Config, name, account string) (string, error) {
	trust, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect":    "Allow",
			"Principal": map[string]string{"AWS": "arn:aws:iam::" + account + ":root"},
			"Action":    "sts:AssumeRole",
		}},
	})
	if err != nil {
		return "", err
	}
	out, err := iam.NewFromConfig(cfg).CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(string(trust)),
		Description:              aws.String("preflight mapping derivation; safe to delete"),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Role.Arn), nil
}

func report(w *os.File, resourceType, op string, res derive.Result) {
	fmt.Fprintf(w, "\n=== %s %s ===\n", resourceType, op)
	fmt.Fprintf(w, "sufficiency: %s   minimality: %s   attempts: %d\n\n",
		res.Sufficiency, res.Minimal, len(res.Attempts))

	if res.Sufficiency != derive.ConfidenceProven {
		fmt.Fprintln(w, "NOT PROVEN — nothing here may be recorded as verified.")
	}
	fmt.Fprintf(w, "derived set (%d):\n", len(res.Sufficient))
	for _, a := range res.Sufficient {
		note := ""
		if ev, ok := res.Evidence[a]; ok && ev.Kind == derive.OutcomeStalled {
			note = "   (omitting this HANGS rather than failing — say so in notes)"
		}
		fmt.Fprintf(w, "  %s%s\n", a, note)
	}
	if len(res.Missing) > 0 {
		fmt.Fprintf(w, "\nMISSING from the mapping (%d) — the dangerous direction:\n", len(res.Missing))
		for _, a := range res.Missing {
			fmt.Fprintf(w, "  + %s\n", a)
		}
	}
	if len(res.Surplus) > 0 {
		fmt.Fprintf(w, "\nSURPLUS in the mapping (%d) — over-reporting:\n", len(res.Surplus))
		for _, a := range res.Surplus {
			fmt.Fprintf(w, "  - %s\n", a)
		}
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "\nwarning: %s\n", warn)
	}
	fmt.Fprintln(w, "\nApply this to the YAML by hand: `verified` is a claim a person makes,")
	fmt.Fprintln(w, "and the PR should say how completeness was established.")
	_ = strings.TrimSpace
}
