package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveConfigDir pins the default. `terraform plan -out` is normally run
// from the configuration root, so plan.json sits beside main.tf and the flag
// exists only for the layouts where it does not.
func TestResolveConfigDir(t *testing.T) {
	tests := map[string]struct {
		flag, plan string
		want       string
	}{
		"flag wins":            {flag: "infra", plan: "out/plan.json", want: "infra"},
		"flag is trimmed":      {flag: "  infra  ", plan: "", want: "infra"},
		"defaults to plan dir": {plan: filepath.Join("infra", "plan.json"), want: "infra"},
		"plan in cwd":          {plan: "plan.json", want: "."},
		"stdin falls back":     {plan: "-", want: "."},
		"no plan falls back":   {want: "."},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := resolveConfigDir(tc.flag, tc.plan); got != tc.want {
				t.Errorf("resolveConfigDir(%q, %q) = %q, want %q", tc.flag, tc.plan, got, tc.want)
			}
		})
	}
}

func TestLoadSourceIndexFindsResources(t *testing.T) {
	var stderr strings.Builder
	ix := loadSourceIndex(filepath.Join("testdata", "config"), "", &stderr)
	if ix == nil {
		t.Fatalf("index is nil; stderr was:\n%s", stderr.String())
	}
	file, line, ok := ix.Lookup("aws_s3_bucket.logs")
	if !ok || line != 1 {
		t.Errorf("Lookup = %q, %d, %v", file, line, ok)
	}
	// The path has to carry the configuration directory, or GitHub looks for
	// the file at the repository root and places nothing.
	if file != "testdata/config/main.tf" {
		t.Errorf("uri = %q, want testdata/config/main.tf", file)
	}
}

// TestLoadSourceIndexDegrades — a configuration preflight cannot read costs
// annotations, never the run. The findings are still correct and still worth
// producing.
func TestLoadSourceIndexDegrades(t *testing.T) {
	for name, dir := range map[string]string{
		"missing directory": filepath.Join("testdata", "does-not-exist"),
		"no .tf files":      "testdata",
	} {
		t.Run(name, func(t *testing.T) {
			var stderr strings.Builder
			if ix := loadSourceIndex(dir, "", &stderr); ix != nil {
				t.Error("an unusable configuration directory produced an index")
			}
			if !strings.Contains(stderr.String(), "warning:") {
				t.Errorf("the degradation was not reported: %q", stderr.String())
			}
		})
	}
}
