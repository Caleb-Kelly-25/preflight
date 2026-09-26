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
	"path/filepath"
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
	// The fixture is COPIED to a temp directory and run from there, rather than
	// run in place. Terraform writes state, a .terraform provider cache and lock
	// files into its working directory, and on this machine derivefixtures/ sits
	// under a synced folder — the sync client held terraform.tfstate open, which
	// broke `terraform init` on a later run and could not be cleared even with a
	// force delete. Working in a copy means nothing Terraform writes ever lands
	// in the repository, so a fixture cannot be poisoned by a previous run.
	workDir, err := copyFixture(*fixtureDir)
	if err != nil {
		return fmt.Errorf("staging the fixture: %w", err)
	}
	defer os.RemoveAll(workDir)

	// State is kept outside the working directory too. On this machine
	// derivefixtures/ sits under a synced folder, and the sync client reading
	// terraform.tfstate mid-write produced a teardown failure that aborted a
	// run and discarded its measurement -- a failure with nothing to do with
	// AWS. It also means a crashed run can still be torn down.
	stateDir, err := os.MkdirTemp("", "preflight-derive-state-")
	if err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	defer os.RemoveAll(stateDir)

	applier := &derive.TerraformApplier{
		Dir:      workDir,
		Region:   *region,
		StateDir: stateDir,
		Creds:    func() derive.Credentials { return grantor.Latest },
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
	res, derr := d.Derive(ctx, seed)
	// Report first: a run that aborted still established something, and the
	// warnings explain why it stopped.
	report(stdout, *resourceType, *operation, res)
	if derr != nil {
		return derr
	}
	if res.Dirty {
		return fmt.Errorf("teardown failed during the run; check for surviving resources")
	}
	return nil
}

// copyFixture stages a fixture in a temp directory. Only the .tf and .hcl files
// are copied: a stale terraform.tfstate carried across would make Terraform
// believe resources exist that do not, and the whole point is a clean run.
func copyFixture(src string) (string, error) {
	dst, err := os.MkdirTemp("", "preflight-derive-fixture-")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return "", err
	}
	var staged int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ext := filepath.Ext(name); ext != ".tf" && ext != ".hcl" && ext != ".json" {
			continue
		}
		if strings.HasPrefix(name, "terraform.tfstate") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o600); err != nil {
			return "", err
		}
		staged++
	}
	if staged == 0 {
		return "", fmt.Errorf("%s contains no .tf files", src)
	}
	return dst, nil
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
	out = append(out, res.ReadActions...)

	// Reference actions must be granted too, or the apply cannot succeed and the
	// run measures nothing.
	//
	// Omitting them was a real bug, found by using this: deriving
	// aws_iam_instance_profile without iam:PassRole did not fail cleanly, it
	// HUNG — the provider retried until the budget expired, so the run reported
	// "stalled, no attributable denial" rather than naming the missing
	// permission. Any entry with references was underivable.
	//
	// The grant is unscoped here (Resource: "*"), so a reference needs nothing
	// special beyond being present.
	for _, ref := range res.References {
		if ref.AppliesTo(op) {
			out = append(out, ref.Action)
		}
	}
	return out, nil
}

// referenceActions is the subset of a seed that came from `references`. The
// report names them, because a derived action list is applied to the YAML by
// hand and these belong under `references` — scoped to another resource's ARN —
// rather than under `operations`. Moving one into operations would check it
// against the wrong resource.
func referenceActions(resourceType string, op mapping.Operation) map[string]bool {
	out := map[string]bool{}
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		return out
	}
	res, ok := db.Lookup(resourceType)
	if !ok {
		return out
	}
	for _, ref := range res.References {
		if ref.AppliesTo(op) {
			out[ref.Action] = true
		}
	}
	return out
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
