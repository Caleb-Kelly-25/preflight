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
		evidenceDir  = fs.String("evidence-dir", filepath.Join("mappings", "evidence"),
			"where to record the run's transcript; empty to skip")
		supportDir = fs.String("support", "",
			"directory holding a support fixture: resources the measured fixture depends on, "+
				"applied once with operator credentials and destroyed after the run")
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

	// Fixtures take the account as a Terraform variable rather than hardcoding
	// it. Two reasons: an account number in a tracked file is information
	// disclosure in a repository destined to be public, and a hardcoded one makes
	// every fixture unusable by anyone but its author — which matters, because
	// mappings/README.md asks outside contributors to derive entries.
	//
	// Set on the process so BOTH the scratch-role apply and the operator destroy
	// inherit it; they build their environments separately.
	if err := os.Setenv("TF_VAR_account_id", account); err != nil {
		return fmt.Errorf("setting TF_VAR_account_id: %w", err)
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

	// SUPPORT FIXTURE. Some resource types cannot be measured alone: an inline role
	// policy needs a role, a log stream needs a log group, an SQS queue policy
	// needs a queue. Creating that dependency INSIDE the measured fixture would be
	// a correctness bug rather than a shortcut — the apply would then also need the
	// dependency's own create actions, the derivation would discover them, and they
	// would land in this resource type's action list although they belong to
	// another type entirely. A derived set only means anything if the fixture
	// exercises exactly one resource type.
	//
	// So a support fixture is a SEPARATE Terraform workspace, applied once with
	// operator credentials and destroyed after the run. It is never measured and
	// the scratch role never applies it: leaving Creds nil means there is no way
	// for this stack to be applied under the scratch role even by mistake.
	//
	// STAGED HERE, ABOVE THE TEARDOWN DEFER, AND THAT ORDER IS LOAD-BEARING.
	// Deferred calls run last-in-first-out, so a `defer os.RemoveAll(...)`
	// registered after the teardown function runs BEFORE it — which deleted the
	// support stack's working directory while `terraform destroy` still needed it,
	// and leaked a real queue. Registering the directory cleanups first means they
	// run last.
	var support *derive.TerraformApplier
	if *supportDir != "" {
		supportWork, err := copyFixture(*supportDir)
		if err != nil {
			return fmt.Errorf("staging the support fixture: %w", err)
		}
		defer os.RemoveAll(supportWork)

		supportState, err := os.MkdirTemp("", "preflight-derive-support-state-")
		if err != nil {
			return fmt.Errorf("creating the support state directory: %w", err)
		}
		defer os.RemoveAll(supportState)

		support = &derive.TerraformApplier{
			Dir:          supportWork,
			Region:       *region,
			StateDir:     supportState,
			SetupApplies: true,
		}
	}

	applier := &derive.TerraformApplier{
		Dir:      workDir,
		Region:   *region,
		StateDir: stateDir,
		Creds:    func() derive.Credentials { return grantor.Latest },
	}

	// What the scratch role does, and therefore what gets measured.
	//
	// A create acts on nothing, so it needs no before-state: the scratch role
	// applies the fixture as written. An update and a delete both act on something
	// that must already exist, so `Setup` creates it with OPERATOR credentials —
	// otherwise the create would be folded into the measurement and there would be
	// no way to separate the two.
	measure := derive.StepApply

	switch *operation {
	case "update":
		// An update needs a BEFORE and an AFTER, so the fixture must be able to
		// express two shapes. Refusing without a `phase` variable is the point:
		// otherwise both phases apply the same configuration, the scratch role
		// performs the create, and the run reports the create path labelled
		// "update".
		phased, err := fixtureHasPhase(workDir)
		if err != nil {
			return fmt.Errorf("inspecting the fixture: %w", err)
		}
		if !phased {
			return fmt.Errorf("deriving %s needs a two-phase fixture: %s declares no `variable \"phase\"`, "+
				"so phase 1 and phase 2 would apply the same configuration and the measurement would be "+
				"the create path mislabelled as %s. See derivefixtures/README.md",
				*operation, *fixtureDir, *operation)
		}
		applier.SetupApplies = true
		applier.SetupVars = []string{"-var", "phase=1"}
		applier.ApplyVars = []string{"-var", "phase=2"}

	case "delete":
		// A delete needs NO second shape and no phase variable: the same fixture
		// is applied with operator credentials and then destroyed by the scratch
		// role. So an ordinary create fixture derives a delete path unchanged,
		// which is why this needs no new fixtures at all.
		applier.SetupApplies = true
		measure = derive.StepDestroy
	}

	// Teardown runs on every exit path, including Ctrl-C, on a context that is
	// not the cancelled one.
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := applier.Destroy(tctx); err != nil {
			fmt.Fprintf(stderr, "WARNING: destroy failed, resources may survive: %v\n", err)
		}
		// After the measured fixture, never before: the measured resource depends on
		// the support one, so tearing the support stack down first would leave the
		// dependent resource undeletable.
		if support != nil {
			if err := support.Destroy(tctx); err != nil {
				fmt.Fprintf(stderr, "WARNING: support fixture destroy failed, resources may survive: %v\n", err)
			}
		}
		if err := grantor.Revoke(tctx); err != nil {
			fmt.Fprintf(stderr, "WARNING: could not remove scratch role %s: %v\n", roleName, err)
		} else {
			fmt.Fprintf(stdout, "\nscratch role %s removed\n", roleName)
		}
	}()

	// Bring the support stack up before the loop starts. Its teardown is armed in
	// the deferred block above, which runs after the measured fixture's.
	if support != nil {
		if err := support.Init(ctx); err != nil {
			return fmt.Errorf("support fixture terraform init: %w", err)
		}
		fmt.Fprintf(stdout, "bringing up the support fixture from %s\n", *supportDir)
		if err := support.Setup(ctx, 15*time.Minute); err != nil {
			return fmt.Errorf("support fixture apply: %w", err)
		}
	}

	if err := applier.Init(ctx); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}

	d := &derive.Deriver{
		Grantor:       grantor,
		Applier:       applier,
		MaxAttempts:   *maxAttempts,
		AttemptBudget: *budget,
		Measure:       measure,
		Log:           stdout,
	}
	res, derr := d.Derive(ctx, seed)
	// Report first: a run that aborted still established something, and the
	// warnings explain why it stopped.
	report(stdout, *resourceType, *operation, res)

	// Record the transcript. This is NOT the same thing as editing the YAML,
	// which the harness still refuses to do: promoting an entry to verified is a
	// claim a person makes and justifies. Recording what a run measured is a
	// transcript, and a hand-written transcript drifts — every evidence file that
	// existed before this code did had to be transcribed from prose notes, and
	// one entry claimed verification with no notes at all.
	//
	// Only a proven run is recorded. An inconclusive one measured nothing that
	// could support a claim, and filing it as evidence would be the same mistake
	// in a new place.
	if *evidenceDir != "" && res.Sufficiency == derive.ConfidenceProven {
		path, err := writeEvidence(*evidenceDir, *resourceType, *operation, *fixtureDir,
			providerVersion(workDir), res)
		if err != nil {
			// A failure here loses the record, not the measurement, which the
			// report above already printed. Worth a warning, not an abort.
			fmt.Fprintf(stderr, "WARNING: could not record evidence: %v\n", err)
		} else {
			fmt.Fprintf(stdout, "\nrecorded in %s\n", path)
		}
	}
	if derr != nil {
		return derr
	}
	if res.Dirty {
		return fmt.Errorf("teardown failed during the run; check for surviving resources")
	}
	return nil
}

// fixtureHasPhase reports whether the fixture declares the `phase` variable that
// a two-phase (before/after) derivation requires.
//
// This is a deliberate string match rather than a full HCL parse. The driver only
// needs to know whether the convention was followed, the fixtures are
// hand-written and a dozen lines long, and pulling an HCL parser into a
// maintainer tool for one question is not worth the dependency. If a fixture ever
// declares the variable in a way this misses, the run fails closed -- it refuses
// to derive rather than deriving the wrong thing.
func fixtureHasPhase(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".tf" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		if strings.Contains(string(body), `variable "phase"`) {
			return true, nil
		}
	}
	return false, nil
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

// claimsOperation reports whether the shipped entry already claims this operation
// as verified. A load failure answers "yes" so that a broken database produces no
// advice rather than misleading advice; the mapping tests are what catch that.
func claimsOperation(resourceType, op string) bool {
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		return true
	}
	res, ok := db.Lookup(resourceType)
	if !ok {
		return true
	}
	for _, claimed := range res.VerifiedOps() {
		if claimed == mapping.Operation(op) {
			return true
		}
	}
	return false
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
