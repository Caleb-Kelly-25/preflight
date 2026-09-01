// Command preflight checks whether the identity running `terraform apply` has
// the IAM permissions a plan actually requires — before the plan is merged.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/internal/plan"
	"github.com/Caleb-Kelly-25/preflight/internal/principal"
	"github.com/Caleb-Kelly-25/preflight/internal/report"
	"github.com/Caleb-Kelly-25/preflight/internal/simulate"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

// version is overridden at release time with -ldflags "-X main.version=v1.2.3".
var version = ""

// Exit codes. These are part of the CI contract and must stay stable.
const (
	exitOK      = 0
	exitFinding = 1
	exitError   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}

	switch args[0] {
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "mappings":
		return runMappings(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, versionString())
		return exitOK
	case "help", "--help", "-h":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return exitError
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `preflight — catch IAM permission gaps before terraform apply

usage:
  preflight check    --plan <plan.json> [flags]
  preflight mappings list
  preflight version

Produce the input with:
  terraform plan -out=tf.plan && terraform show -json tf.plan > plan.json

preflight uses the AWS credentials already configured in your shell or CI job.
It needs three read-only permissions; run without them and it will print the
exact policy to add.

Run "preflight check -h" for check's flags.
`)
}

// contextFlag collects repeated --context KEY=VALUE or KEY@TYPE=VALUE pairs.
//
// The type is delimited with "@" rather than ":" because IAM context keys
// contain colons themselves ("aws:SourceIp"), which would make a colon
// delimiter ambiguous. "@" appears in no AWS context key name.
type contextFlag []finding.ContextEntry

func (c *contextFlag) String() string { return "" }

func (c *contextFlag) Set(s string) error {
	spec, value, ok := strings.Cut(s, "=")
	if !ok || spec == "" {
		return fmt.Errorf("want KEY=VALUE or KEY@TYPE=VALUE, got %q", s)
	}
	key, typeName, explicit := strings.Cut(spec, "@")
	if key == "" {
		return fmt.Errorf("context key is empty in %q", s)
	}

	// The type is not cosmetic: the simulator rejects an entry whose type does
	// not match how the policy uses the key, so an ip-valued key sent as a
	// string is simply refused. Defaulting to string is safe; guessing from the
	// key name would not be.
	typ := finding.ContextString
	if explicit {
		typ = finding.ContextValueType(typeName)
		if !finding.ValidContextValueType(typ) {
			return fmt.Errorf("unknown context type %q in %q (want string, stringList, numeric, boolean, date, ip, or arn)", typeName, s)
		}
	}

	values := []string{value}
	switch {
	case typ == finding.ContextStringList:
		values = strings.Split(value, ",")
	case !explicit && strings.Contains(value, ","):
		// A comma was previously taken as a silent list separator, which both
		// mangled values that legitimately contain one and hid the fact that a
		// type was being inferred. The ambiguity is the caller's to resolve.
		return fmt.Errorf("value for %q contains a comma: pass %s@stringList=%s for a list, or %s@string=%s to keep it literal",
			key, key, value, key, value)
	}

	*c = append(*c, finding.ContextEntry{Key: key, Type: typ, Values: values})
	return nil
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		planPath     = fs.String("plan", "", "path to `terraform show -json` output, or - for stdin (required)")
		format       = fs.String("format", "text", "output format: text, json, or sarif")
		failOn       = fs.String("fail-on", "denied", "exit non-zero on: denied, likely, or unchecked")
		region       = fs.String("region", "", "AWS region; defaults to the plan's provider config, then the environment")
		principalARN = fs.String("principal", "", "caller ARN to check; defaults to the current identity via sts:GetCallerIdentity")
		explain      = fs.Bool("explain", false, "show the context supplied and the raw decision for each action")
		timeout      = fs.Duration("timeout", 5*time.Minute, "overall budget for AWS calls")
		extraContext contextFlag
	)
	fs.Var(&extraContext, "context", "supply a condition key value, repeatable: --context aws:SourceIp@ip=10.0.0.1 (type defaults to string)")

	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if *planPath == "" {
		fmt.Fprintln(stderr, "error: --plan is required")
		fs.Usage()
		return exitError
	}

	outFormat, err := report.ParseFormat(*format)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	threshold := finding.FailThreshold(*failOn)
	switch threshold {
	case finding.FailOnDenied, finding.FailOnLikely, finding.FailOnUnchecked:
	default:
		fmt.Fprintf(stderr, "error: unknown --fail-on %q (want denied, likely, or unchecked)\n", *failOn)
		return exitError
	}

	// Parse the plan first: it may carry the region everything else needs.
	src, closeSrc, err := openPlan(*planPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	p, err := plan.Parse(src)
	closeSrc()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	resolvedRegion := resolveRegion(*region, p)

	db, err := mapping.Load(mappings.FS)
	if err != nil {
		fmt.Fprintf(stderr, "error: loading mapping database: %v\n", err)
		return exitError
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	opts := engine.Options{
		Database:     db,
		Region:       resolvedRegion,
		ExtraContext: extraContext,
	}

	if code := connect(ctx, &opts, *principalARN, resolvedRegion, stderr); code != exitOK {
		return code
	}

	rep, err := engine.Analyze(ctx, p, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	if err := report.Write(stdout, rep, outFormat, report.WriteOptions{Explain: *explain}); err != nil {
		if errors.Is(err, report.ErrSARIFUnimplemented) {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stderr, "error: writing report: %v\n", err)
		return exitError
	}

	if rep.ShouldFail(threshold) {
		return exitFinding
	}
	return exitOK
}

// connect resolves the identity and wires up the simulator, or explains
// precisely why it cannot.
//
// It fills in opts.Identity, opts.Simulator and opts.PolicyContextKeys.
func connect(ctx context.Context, opts *engine.Options, principalARN, region string, stderr io.Writer) int {
	cfg, cfgErr := simulate.LoadConfig(ctx, region, 0)

	// An explicit --principal with no working AWS access is a legitimate
	// offline dry run: everything reports Unchecked, honestly.
	if cfgErr != nil {
		if principalARN == "" {
			fmt.Fprintf(stderr, "error: %v\n", cfgErr)
			return exitError
		}
		id, err := principal.Resolve(principalARN)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		opts.Identity = id
		fmt.Fprintf(stderr, "warning: no usable AWS configuration (%v); running offline, every result will be Unchecked\n", cfgErr)
		return exitOK
	}

	client := simulate.New(cfg, simulate.Options{})

	// Identity.
	var id principal.Identity
	if principalARN != "" {
		var err error
		if id, err = principal.Resolve(principalARN); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
	} else {
		var err error
		id, err = simulate.ResolveIdentity(ctx, client.STS())
		if err != nil {
			if errors.Is(err, simulate.ErrNoCredentials) {
				fmt.Fprintln(stderr, "error: no AWS credentials found.")
				fmt.Fprintln(stderr, "  preflight uses the credentials already configured in your shell or CI job.")
				fmt.Fprintln(stderr, "  To check without AWS access, pass --principal <arn> for an offline dry run")
				fmt.Fprintln(stderr, "  (every result will report Unchecked).")
				return exitError
			}
			return reportAWSError(stderr, err, "")
		}
	}
	opts.Identity = id

	if !id.Simulatable() {
		fmt.Fprintf(stderr, "error: cannot check this identity: %s\n", id.Reason())
		return exitError
	}

	// The context-key probe. Required, not optional: it is the only signal that
	// a condition key is in play, and without it an "allowed" may be resting on
	// a value AWS substituted for us. See docs/DESIGN.md §7.3.
	keys, err := simulate.ReferencedContextKeys(ctx, client.IAM(), id.PolicySourceARN)
	if err != nil {
		return reportAWSError(stderr, err, id.PolicySourceARN)
	}

	opts.PolicyContextKeys = keys
	opts.Simulator = client
	return exitOK
}

// reportAWSError turns a fatal AWS failure into an actionable message.
func reportAWSError(stderr io.Writer, err error, principalARN string) int {
	var perm *simulate.PermissionError
	if errors.As(err, &perm) {
		fmt.Fprintf(stderr, "error: %v\n\n", perm)
		fmt.Fprintln(stderr, "preflight needs three read-only permissions. Add this policy to the identity")
		fmt.Fprintln(stderr, "running preflight:")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, simulate.RequiredPolicyJSON(principalARN))
		return exitError
	}

	var missing *simulate.PrincipalNotFoundError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "error: %v\n", missing)
		fmt.Fprintln(stderr, "  The principal preflight resolved does not exist. If your deploy role is")
		fmt.Fprintln(stderr, "  assumed indirectly, pass it explicitly with --principal <role-arn>.")
		return exitError
	}

	fmt.Fprintf(stderr, "error: %v\n", err)
	return exitError
}

// resolveRegion picks the region to build ARNs from and to supply as
// aws:RequestedRegion. Flag wins, then the plan's provider config, then the
// environment.
func resolveRegion(flagValue string, p *plan.Plan) string {
	if r := strings.TrimSpace(flagValue); r != "" {
		return r
	}
	if r, ok := p.ProviderRegion(); ok {
		return r
	}
	for _, env := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		if r := strings.TrimSpace(os.Getenv(env)); r != "" {
			return r
		}
	}
	return ""
}

func openPlan(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening plan file: %w", err)
	}
	return f, func() { _ = f.Close() }, nil
}

func runMappings(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "list" {
		fmt.Fprintln(stderr, "usage: preflight mappings list")
		return exitError
	}
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	types := db.Types()
	var verified int
	for _, t := range types {
		r, _ := db.Lookup(t)
		if r.Verified() {
			verified++
		}
		ops := make([]string, 0, len(r.Operations))
		for _, op := range []mapping.Operation{mapping.OpCreate, mapping.OpUpdate, mapping.OpDelete} {
			if len(r.Operations[op]) > 0 {
				ops = append(ops, string(op))
			}
		}
		fmt.Fprintf(stdout, "%-52s %-8s %s\n", t, r.Status, strings.Join(ops, ","))
	}
	fmt.Fprintf(stdout, "\n%d resource types mapped, %d verified\n", len(types), verified)
	return exitOK
}

func versionString() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "dev"
}
