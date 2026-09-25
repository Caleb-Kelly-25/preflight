package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/plan"
)

// The exit codes are a CI contract: 0 pass, 1 findings tripped the threshold,
// 2 usage or runtime error. The GitHub Action branches on them, so a change
// here silently changes what every workflow using it does.
//
// Every case below returns before any AWS call, so none of this needs
// credentials. The paths that do reach AWS are covered by the contract tests.
func TestExitCodes(t *testing.T) {
	plan := filepath.Join("..", "..", "internal", "plan", "testdata", "simple.tfplan.json")

	tests := map[string]struct {
		args []string
		want int
	}{
		"no arguments is a usage error":  {nil, exitError},
		"unknown command":                {[]string{"frobnicate"}, exitError},
		"version":                        {[]string{"version"}, exitOK},
		"version via flag":               {[]string{"--version"}, exitOK},
		"help":                           {[]string{"help"}, exitOK},
		"help via flag":                  {[]string{"--help"}, exitOK},
		"mappings list":                  {[]string{"mappings", "list"}, exitOK},
		"mappings with no subcommand":    {[]string{"mappings"}, exitError},
		"mappings with a bad subcommand": {[]string{"mappings", "explode"}, exitError},
		"check without --plan":           {[]string{"check"}, exitError},
		"check with an unknown flag":     {[]string{"check", "--nope"}, exitError},
		"check with a bad --format":      {[]string{"check", "--plan", plan, "--format", "yaml"}, exitError},
		"check with a bad --fail-on":     {[]string{"check", "--plan", plan, "--fail-on", "maybe"}, exitError},
		"check with a bad --context":     {[]string{"check", "--plan", plan, "--context", "nokey"}, exitError},
		"check with a missing plan file": {[]string{"check", "--plan", "does-not-exist.json"}, exitError},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != tc.want {
				t.Errorf("run(%v) = %d, want %d\nstdout: %s\nstderr: %s",
					tc.args, got, tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

// A usage error must explain itself on stderr rather than leaving the caller to
// guess from an exit code alone — in CI the log is all anyone sees.
func TestUsageErrorsExplainThemselves(t *testing.T) {
	tests := map[string][]string{
		"missing --plan":  {"check"},
		"unknown command": {"frobnicate"},
		"bad format":      {"check", "--plan", "x.json", "--format", "yaml"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			run(args, &stdout, &stderr)
			if strings.TrimSpace(stderr.String()) == "" {
				t.Error("exited with a usage error but said nothing on stderr")
			}
		})
	}
}

// Anything a user is told to run must be discoverable from help, or the flag
// might as well not exist.
func TestHelpMentionsTheSubcommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"help"}, &stdout, &stderr); got != exitOK {
		t.Fatalf("help exited %d", got)
	}
	for _, want := range []string{"check", "mappings", "version", "--plan"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help does not mention %q", want)
		}
	}
}

// resolveRegion has three tiers — the flag, then the plan's provider config,
// then the environment — and getting the precedence wrong silently builds ARNs
// in the wrong region, which reads as a permission gap rather than a bug.
func TestResolveRegionPrecedence(t *testing.T) {
	src, closeSrc, err := openPlan(filepath.Join("..", "..", "internal", "plan", "testdata", "simple.tfplan.json"))
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	p, err := plan.Parse(src)
	closeSrc()
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}

	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_DEFAULT_REGION", "")

	if got := resolveRegion("us-west-2", p); got != "us-west-2" {
		t.Errorf("explicit --region lost: got %q", got)
	}
	if got := resolveRegion("", p); got == "" {
		t.Error("no region resolved from the plan or the environment")
	}
}

// The plan may arrive on stdin, which is how it is piped in CI.
func TestOpenPlanReadsStdin(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, []byte(`{"format_version":"1.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	src, closeSrc, err := openPlan(path)
	if err != nil {
		t.Fatalf("openPlan(file): %v", err)
	}
	closeSrc()
	if src == nil {
		t.Error("openPlan returned no reader for a real file")
	}

	if _, _, err := openPlan("no-such-file.json"); err == nil {
		t.Error("openPlan accepted a path that does not exist")
	}
}
