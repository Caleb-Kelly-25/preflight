//go:build awsderive

package derive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// errorsAs is a tiny indirection so awsgrant.go does not import errors twice.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// TerraformApplier drives the terraform binary.
//
// Shelling out rather than importing a Terraform library is deliberate:
// CLAUDE.md's dependency list is closed, this tool runs in other people's CI
// with live credentials, and a maintainer-only convenience does not justify
// widening the dependency surface.
type TerraformApplier struct {
	Dir string
	// Creds supplies the scratch role's credentials for apply. Destroy
	// deliberately does not use them.
	Creds func() Credentials
	// Region is passed through to the provider.
	Region string
	// DestroyBudget bounds teardown independently of the apply budget.
	DestroyBudget time.Duration

	// StateDir holds Terraform state, deliberately OUTSIDE the fixture
	// directory. Two reasons, one of which cost a whole derivation run.
	//
	// The one that bit: derivefixtures/ lives under a synced folder on this
	// machine, and OneDrive reads terraform.tfstate while Terraform is writing
	// it. Terraform then fails with "the process cannot access the file because
	// another process has locked a portion of the file" -- a teardown failure
	// with nothing to do with AWS, which aborts the run and discards the
	// measurement.
	//
	// The other: state outside the working tree survives the working tree, so a
	// crashed run can still be torn down.
	//
	// Empty means the fixture directory, which is the old behaviour.
	StateDir string

	// SetupVars and ApplyVars are extra `terraform` arguments selecting which
	// shape of the fixture to apply. They are how an update is measured: the
	// fixture takes a `phase` variable, Setup applies phase 1 as the operator to
	// establish the resource, and Apply applies phase 2 as the scratch role to
	// perform the change being measured.
	//
	// Both empty means a create: Setup does nothing and Apply applies the fixture
	// as written.
	SetupVars []string
	ApplyVars []string

	// SetupApplies makes Setup apply the fixture with OPERATOR credentials before
	// the measured step runs.
	//
	// Explicit rather than inferred from SetupVars, because a DELETE measurement
	// needs a before-state and no variables at all: the same fixture is applied
	// and then destroyed. Inferring it would have silently skipped Setup for every
	// delete, leaving the scratch role destroying nothing and every attempt
	// reporting success.
	//
	// Wrong for a create, where it would create the very resource being measured.
	SetupApplies bool
}

// Setup applies the "before" shape with OPERATOR credentials.
//
// No-op unless SetupApplies is set, which is the create case: there is nothing
// for a create to act on, and applying the fixture here would create the very
// resource the measured apply is supposed to create.
func (t *TerraformApplier) Setup(ctx context.Context, budget time.Duration) error {
	if !t.SetupApplies {
		return nil
	}
	args := append([]string{"apply", "-auto-approve", "-input=false", "-no-color"}, t.stateArgs()...)
	args = append(args, t.SetupVars...)
	out, timedOut, err := t.run(ctx, budget, t.operatorEnv(), args...)
	if timedOut {
		return errors.New("setup apply timed out")
	}
	if err != nil {
		return fmt.Errorf("setup apply failed: %w: %s", err, tail(out))
	}
	return nil
}

// stateArgs points Terraform at StateDir when one is set. Passed to apply and
// destroy alike: a destroy that cannot see the state cannot tear anything down.
func (t *TerraformApplier) stateArgs() []string {
	if t.StateDir == "" {
		return nil
	}
	return []string{"-state=" + filepath.Join(t.StateDir, "terraform.tfstate")}
}

// Apply runs terraform apply under the scratch role, classifying the result.
func (t *TerraformApplier) Apply(ctx context.Context, budget time.Duration) Outcome {
	start := time.Now()
	args := append([]string{"apply", "-auto-approve", "-input=false", "-no-color"}, t.stateArgs()...)
	args = append(args, t.ApplyVars...)
	out, timedOut, err := t.run(ctx, budget, t.applyEnv(), args...)
	elapsed := time.Since(start)

	if timedOut {
		// A stall, not a failure. See OutcomeStalled: a missing permission can
		// make the provider retry forever rather than error.
		return Outcome{
			Kind:      OutcomeStalled,
			StalledAt: lastResourceMentioned(out),
			Elapsed:   elapsed,
			Detail:    tail(out),
		}
	}
	if err == nil {
		return Outcome{Kind: OutcomeSuccess, Elapsed: elapsed}
	}

	return classify(out, elapsed)
}

// classify turns one terraform run's output into an Outcome.
//
// Shared by Apply and MeasureDestroy on purpose. The two measure opposite ends of
// a resource's life but the evidence reads identically — a named denial, an opaque
// refusal, a stall, or an unrelated failure — and two copies of this logic would
// eventually disagree about what counts as a denial. That disagreement would be
// invisible: a delete path would quietly classify a real denial as a plain
// failure, and the loop would report "failed for a non-IAM reason" for a missing
// permission.
func classify(out string, elapsed time.Duration) Outcome {
	actions, opaque := ParseDenial(out)
	switch {
	case len(actions) > 0:
		return Outcome{Kind: OutcomeDenied, DeniedActions: actions, Elapsed: elapsed, Detail: tail(out)}
	case opaque:
		return Outcome{Kind: OutcomeOpaque, Elapsed: elapsed, Detail: tail(out)}
	default:
		return Outcome{Kind: OutcomeFailed, Elapsed: elapsed, Detail: tail(out)}
	}
}

// MeasureDestroy destroys under the SCRATCH role and reports the outcome. See the
// Applier interface for why this is separate from Destroy.
//
// The caller must still call Destroy afterwards. A denied destroy leaves the
// resource standing — that is the whole point of the measurement — so skipping the
// operator cleanup would leak exactly when a leak is guaranteed.
func (t *TerraformApplier) MeasureDestroy(ctx context.Context, budget time.Duration) Outcome {
	start := time.Now()
	args := append([]string{"destroy", "-auto-approve", "-input=false", "-no-color"}, t.stateArgs()...)
	args = append(args, t.ApplyVars...)
	out, timedOut, err := t.run(ctx, budget, t.applyEnv(), args...)
	elapsed := time.Since(start)

	if timedOut {
		// Same hazard as a stalled apply, and worse in one respect: the destroy
		// was killed mid-flight, so some resources may be gone and unrecorded
		// while others remain. The loop treats a stall as evidence and then stops.
		return Outcome{
			Kind:      OutcomeStalled,
			StalledAt: lastResourceMentioned(out),
			Elapsed:   elapsed,
			Detail:    tail(out),
		}
	}
	if err == nil {
		return Outcome{Kind: OutcomeSuccess, Elapsed: elapsed}
	}
	return classify(out, elapsed)
}

// Destroy tears the fixture down with OPERATOR credentials, never the scratch
// role's. Teardown must not be able to fail for want of the very permission the
// loop is searching for — an under-permissioned destroy is a stuck destroy, and
// a leaked resource makes the next attempt lie.
func (t *TerraformApplier) Destroy(ctx context.Context) error {
	budget := t.DestroyBudget
	if budget <= 0 {
		budget = 10 * time.Minute
	}
	dargs := append([]string{"destroy", "-auto-approve", "-input=false", "-no-color"}, t.stateArgs()...)
	// Destroy must see the same variables, or a fixture whose shape depends on
	// them cannot be planned for deletion.
	dargs = append(dargs, t.ApplyVars...)
	out, timedOut, err := t.run(ctx, budget, t.operatorEnv(), dargs...)
	if err == nil && !timedOut {
		return nil
	}

	// A stalled apply is killed mid-flight, which can leave the state lock held
	// by a provider process that is now gone. The next destroy then fails for a
	// reason that has nothing to do with AWS. Break the lock and retry once —
	// safe here because the harness owns this working directory exclusively.
	if id := lockID(out); id != "" {
		if _, _, unlockErr := t.run(ctx, time.Minute, t.operatorEnv(),
			"force-unlock", "-force", id); unlockErr != nil {
			return fmt.Errorf("destroy blocked by state lock %s and force-unlock failed: %w", id, unlockErr)
		}
		out, timedOut, err = t.run(ctx, budget, t.operatorEnv(), dargs...)
		if err == nil && !timedOut {
			return nil
		}
	}

	if timedOut {
		return errors.New("terraform destroy timed out")
	}
	return fmt.Errorf("terraform destroy failed: %w: %s", err, tail(out))
}

// lockID pulls the lock identifier out of Terraform's own error text, which is
// the only place it is reported.
func lockID(out string) string {
	m := lockIDPattern.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

var lockIDPattern = regexp.MustCompile(`(?i)Lock Info:[\s\S]*?ID:\s+([0-9a-fA-F-]{8,})`)

// Init prepares the working directory. Operator credentials; no resources are
// created.
func (t *TerraformApplier) Init(ctx context.Context) error {
	_, _, err := t.run(ctx, 5*time.Minute, t.operatorEnv(), "init", "-input=false", "-no-color")
	return err
}

func (t *TerraformApplier) run(ctx context.Context, budget time.Duration, env []string, args ...string) (string, bool, error) {
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "terraform", args...)
	cmd.Dir = t.Dir
	cmd.Env = env

	// Terraform spawns provider plugins as children, and those children hold the
	// state file open. CommandContext's default kill signals only terraform
	// itself, so a stalled apply leaves a plugin running and the NEXT destroy
	// fails on a locked state file — a teardown failure with nothing to do with
	// AWS, which aborts the run and discards the measurement.
	//
	// Cancel replaces that default with a whole-tree kill. See proc_windows.go
	// and proc_unix.go. WaitDelay then bounds how long Wait will linger on
	// inherited pipes after the tree is gone.
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.WaitDelay = 10 * time.Second

	out, err := cmd.CombinedOutput()
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	return string(out), timedOut, err
}

func (t *TerraformApplier) baseEnv() []string {
	env := os.Environ()
	// Strip inherited AWS credentials so neither path can accidentally run as
	// whoever happens to be configured.
	cleaned := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "AWS_ACCESS_KEY_ID=") ||
			strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY=") ||
			strings.HasPrefix(kv, "AWS_SESSION_TOKEN=") {
			continue
		}
		cleaned = append(cleaned, kv)
	}
	if t.Region != "" {
		cleaned = append(cleaned, "AWS_REGION="+t.Region)
	}
	return cleaned
}

// applyEnv runs as the scratch role.
func (t *TerraformApplier) applyEnv() []string {
	env := t.baseEnv()
	if t.Creds == nil {
		return env
	}
	c := t.Creds()
	if c.AccessKeyID == "" {
		// No credentials means the previous Grant failed. Returning the
		// operator's environment here would run the apply with full
		// permissions and report every action as unnecessary.
		return append(env, "AWS_ACCESS_KEY_ID=invalid-derive-guard")
	}
	return append(env,
		"AWS_ACCESS_KEY_ID="+c.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+c.SecretAccessKey,
		"AWS_SESSION_TOKEN="+c.SessionToken)
}

// operatorEnv keeps whatever credentials the process itself was started with,
// by not overriding them.
func (t *TerraformApplier) operatorEnv() []string { return os.Environ() }

// lastResourceMentioned finds the address terraform was working on when it
// stalled, so a hang points somewhere rather than nowhere.
func lastResourceMentioned(out string) string {
	var last string
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, ": Still creating..."); i > 0 {
			last = strings.TrimSpace(line[:i])
		}
	}
	return last
}

func tail(s string) string {
	const max = 2000
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max:]
}

// Outputs returns this workspace's Terraform outputs, flattened to strings.
//
// It exists so a SUPPORT fixture can hand generated identifiers to the measured
// fixture. Most support resources can be referred to by a fixed name the two
// fixtures agree on, but some cannot: a Route 53 zone id, a KMS key id and an ELB
// ARN are all assigned by AWS and unknowable before the apply.
//
// The alternative was a `data` source in the measured fixture looking the parent up
// by name — and that would be a measurement bug, not a style choice. Data sources
// are read under the SCRATCH role, so the lookup's own permission
// (route53:ListHostedZonesByName, say) would be discovered by the loop and
// attributed to the resource type under test, which does not need it.
//
// Runs with operator credentials: a support stack is never touched by the scratch
// role.
func (t *TerraformApplier) Outputs(ctx context.Context) (map[string]string, error) {
	args := append([]string{"output", "-json", "-no-color"}, t.stateArgs()...)
	out, timedOut, err := t.run(ctx, 2*time.Minute, t.operatorEnv(), args...)
	if timedOut {
		return nil, errors.New("reading terraform outputs timed out")
	}
	if err != nil {
		return nil, fmt.Errorf("reading terraform outputs: %w: %s", err, tail(out))
	}

	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parsing terraform outputs: %w", err)
	}

	vals := make(map[string]string, len(raw))
	for name, o := range raw {
		switch v := o.Value.(type) {
		case string:
			vals[name] = v
		case bool:
			vals[name] = strconv.FormatBool(v)
		case float64:
			// Terraform numbers arrive as float64. Render integers without a
			// trailing ".0", since these become resource names and ids.
			vals[name] = strconv.FormatFloat(v, 'f', -1, 64)
		default:
			// Lists and maps have no single sensible TF_VAR spelling, and guessing
			// one would fail deep inside a later apply. Refuse clearly instead.
			return nil, fmt.Errorf("support output %q is %T; only strings, numbers and bools can be passed to the measured fixture", name, o.Value)
		}
	}
	return vals, nil
}
