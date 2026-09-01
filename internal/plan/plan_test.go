package plan

import (
	"os"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) *Plan {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()

	p, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestParse(t *testing.T) {
	p := loadFixture(t, "simple.tfplan.json")

	if p.TerraformVersion != "1.9.5" {
		t.Errorf("TerraformVersion = %q", p.TerraformVersion)
	}
	if p.UnsupportedFormat() {
		t.Errorf("format version %q reported as unsupported", p.FormatVersion)
	}
	if len(p.ResourceChanges) != 7 {
		t.Fatalf("got %d resource changes, want 7", len(p.ResourceChanges))
	}
}

func TestParseRejectsNonPlanJSON(t *testing.T) {
	// A common user error: piping the state file, or the plan binary, or the
	// output of plain `terraform plan`. Each should produce a clear failure
	// rather than an empty, falsely-clean report.
	for name, input := range map[string]string{
		"empty object":  `{}`,
		"state file":    `{"version": 4, "terraform_version": "1.9.5", "resources": []}`,
		"not json":      `Terraform will perform the following actions:`,
		"empty input":   ``,
		"json fragment": `{"format_version":`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Error("Parse accepted input that is not a plan document")
			}
		})
	}
}

func TestActionable(t *testing.T) {
	p := loadFixture(t, "simple.tfplan.json")
	got := p.Actionable()

	want := map[string]bool{
		"aws_s3_bucket.logs":            true,
		"aws_s3_bucket.artifacts":       true,
		"aws_iam_role.deploy":           true,
		"aws_dynamodb_table.state_lock": true,
	}
	if len(got) != len(want) {
		t.Fatalf("Actionable() returned %d changes, want %d: %v", len(got), len(want), addresses(got))
	}
	for _, rc := range got {
		if !want[rc.Address] {
			t.Errorf("Actionable() included %q, which should have been filtered out", rc.Address)
		}
	}
}

func addresses(cs []ResourceChange) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Address)
	}
	return out
}

func TestActionableFilters(t *testing.T) {
	p := loadFixture(t, "simple.tfplan.json")
	byAddr := map[string]ResourceChange{}
	for _, rc := range p.ResourceChanges {
		byAddr[rc.Address] = rc
	}

	cases := map[string]struct {
		addr string
		want bool
		why  string
	}{
		"data source":    {"data.aws_caller_identity.current", false, "data sources need no apply-time write permission"},
		"no-op":          {"aws_s3_bucket.unchanged", false, "no-op changes call nothing"},
		"other provider": {"random_pet.suffix", false, "non-AWS providers are out of scope"},
		"managed create": {"aws_s3_bucket.logs", true, ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rc, ok := byAddr[tc.addr]
			if !ok {
				t.Fatalf("fixture is missing %q", tc.addr)
			}
			if got := rc.Actionable(); got != tc.want {
				t.Errorf("Actionable() = %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

func TestOperations(t *testing.T) {
	tests := []struct {
		name    string
		actions []Action
		want    []Action
		replace bool
	}{
		{"create", []Action{ActionCreate}, []Action{ActionCreate}, false},
		{"update", []Action{ActionUpdate}, []Action{ActionUpdate}, false},
		{"delete", []Action{ActionDelete}, []Action{ActionDelete}, false},
		// A replacement needs BOTH permissions. Collapsing it to one would let
		// a plan pass that fails halfway through apply, leaving state torn.
		{"replace", []Action{ActionDelete, ActionCreate}, []Action{ActionCreate, ActionDelete}, true},
		{"create before destroy", []Action{ActionCreate, ActionDelete}, []Action{ActionCreate, ActionDelete}, true},
		{"no-op", []Action{ActionNoOp}, nil, false},
		{"read", []Action{ActionRead}, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Change{Actions: tt.actions}
			got := c.Operations()
			if len(got) != len(tt.want) {
				t.Fatalf("Operations() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Operations()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
			if c.IsReplace() != tt.replace {
				t.Errorf("IsReplace() = %v, want %v", c.IsReplace(), tt.replace)
			}
		})
	}
}

func TestUnsupportedFormat(t *testing.T) {
	for version, unsupported := range map[string]bool{
		"1.0": false,
		"1.2": false,
		"1.9": false,
		"2.0": true,
		"0.2": true,
	} {
		p := &Plan{FormatVersion: version}
		if got := p.UnsupportedFormat(); got != unsupported {
			t.Errorf("format %q: UnsupportedFormat() = %v, want %v", version, got, unsupported)
		}
	}
}
