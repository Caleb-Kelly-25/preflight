//go:build awsderive

package derive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
}

// Apply runs terraform apply under the scratch role, classifying the result.
func (t *TerraformApplier) Apply(ctx context.Context, budget time.Duration) Outcome {
	start := time.Now()
	out, timedOut, err := t.run(ctx, budget, t.applyEnv(), "apply", "-auto-approve", "-input=false", "-no-color")
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

// Destroy tears the fixture down with OPERATOR credentials, never the scratch
// role's. Teardown must not be able to fail for want of the very permission the
// loop is searching for — an under-permissioned destroy is a stuck destroy, and
// a leaked resource makes the next attempt lie.
func (t *TerraformApplier) Destroy(ctx context.Context) error {
	budget := t.DestroyBudget
	if budget <= 0 {
		budget = 10 * time.Minute
	}
	out, timedOut, err := t.run(ctx, budget, t.operatorEnv(), "destroy", "-auto-approve", "-input=false", "-no-color")
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
		out, timedOut, err = t.run(ctx, budget, t.operatorEnv(),
			"destroy", "-auto-approve", "-input=false", "-no-color")
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
	// Terraform spawns provider plugins that hold the state lock. WaitDelay
	// gives them a moment to die with the parent rather than being orphaned.
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
