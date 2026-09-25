package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/report"
)

// fakeIndex stands in for internal/hclsrc. The real index resolves count and
// for_each instances back to their block; that is its job and it is tested
// there. What matters here is that WriteSARIF hands over the address unaltered
// and believes the answer.
type fakeIndex map[string]int

func (f fakeIndex) Lookup(address string) (string, int, bool) {
	line, ok := f[address]
	return "main.tf", line, ok
}

// sampleIndex locates every finding in sample() except the unmapped table,
// which is what exercises the degradation path.
func sampleIndex() fakeIndex {
	return fakeIndex{
		"aws_s3_bucket.denied":   4,
		"aws_s3_bucket.wildcard": 9,
		"aws_iam_role.gated":     14,
		"aws_iam_role.clean":     19,
	}
}

func renderSARIF(t *testing.T, r *finding.Report, opts report.WriteOptions) string {
	t.Helper()
	var b strings.Builder
	if err := report.Write(&b, r, report.FormatSARIF, opts); err != nil {
		t.Fatalf("WriteSARIF: %v", err)
	}
	return b.String()
}

// sarifDoc is the shape the assertions below need. It is decoded rather than
// string-matched so a test failure points at the property, not at a line of
// JSON.
type sarifDoc struct {
	Schema  string `json:"$schema"`
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name            string `json:"name"`
				SemanticVersion string `json:"semanticVersion"`
				Rules           []struct {
					ID string `json:"id"`
				} `json:"rules"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID    string `json:"ruleId"`
			RuleIndex int    `json:"ruleIndex"`
			Level     string `json:"level"`
			Message   struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
			PartialFingerprints map[string]string `json:"partialFingerprints"`
		} `json:"results"`
		Invocations []struct {
			ExecutionSuccessful        bool `json:"executionSuccessful"`
			ToolExecutionNotifications []struct {
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
			} `json:"toolExecutionNotifications"`
		} `json:"invocations"`
	} `json:"runs"`
}

func decodeSARIF(t *testing.T, out string) sarifDoc {
	t.Helper()
	var doc sarifDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if doc.Version != "2.1.0" {
		t.Errorf("version = %q, want 2.1.0", doc.Version)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(doc.Runs))
	}
	return doc
}

func TestSARIFGolden(t *testing.T) {
	golden(t, "report.sarif", renderSARIF(t, sample(), report.WriteOptions{
		Locations:   sampleIndex(),
		ToolVersion: "v1.2.3",
	}))
}

// TestSARIFTotalDropIsAnError is the false-pass guard for this format.
//
// GitHub's supported-properties list includes neither executionSuccessful nor
// toolExecutionNotifications (checked 2026-09-25), so an empty SARIF uploaded
// from a passing step is indistinguishable from a clean scan no matter what we
// write into the document. The exit code is the only signal left, so a run that
// could locate nothing must fail rather than emit an empty report.
func TestSARIFTotalDropIsAnError(t *testing.T) {
	var warned []string
	var buf bytes.Buffer
	err := report.WriteSARIF(&buf, sample(), report.WriteOptions{
		ToolVersion: "v1.2.3",
		Warn:        func(s string) { warned = append(warned, s) },
	})
	if err == nil {
		t.Fatal("WriteSARIF succeeded with no locatable findings; an empty report reads as clean")
	}
	if !strings.Contains(err.Error(), "--config-dir") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "--config-dir") {
		t.Errorf("the drop was not also reported to the terminal: %v", warned)
	}
}

// TestSARIFLevels pins the confidence -> level mapping, including the one that
// is an omission rather than a level.
func TestSARIFLevels(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{Locations: sampleIndex()}))
	run := doc.Runs[0]

	levels := map[string]string{}
	for _, res := range run.Results {
		// Every result must carry its address, because the annotation is read
		// inline with none of the report's structure around it.
		for addr := range sampleIndex() {
			if strings.HasPrefix(res.Message.Text, addr+" (") {
				levels[addr] = res.Level
			}
		}
	}

	want := map[string]string{
		"aws_s3_bucket.denied":   "error",   // conclusive denial
		"aws_s3_bucket.wildcard": "warning", // unchecked
		"aws_iam_role.gated":     "note",    // likely
	}
	for addr, level := range want {
		if levels[addr] != level {
			t.Errorf("%s: level = %q, want %q", addr, levels[addr], level)
		}
	}

	// A verified pass is not a problem, and SARIF results are problems.
	if _, present := levels["aws_iam_role.clean"]; present {
		t.Error("a Verified finding was emitted as a SARIF result")
	}
}

// TestSARIFMessagesAreActionable — an annotation the reviewer cannot act on is
// an annotation they learn to dismiss.
func TestSARIFMessagesAreActionable(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{Locations: sampleIndex()}))

	var joined []string
	for _, res := range doc.Runs[0].Results {
		joined = append(joined, res.Message.Text)
	}
	all := strings.Join(joined, "\n")

	for _, want := range []string{
		"s3:CreateBucket",                 // the missing action is named
		"create",                          // the operation is named
		"resource policy not evaluated",   // the reason is named
		"policy conditions not evaluated", // ... for every non-verified finding
		"aws:SourceIp",                    // including which key was missing
		"AWS denied 1 action(s)",          // a suppressed denial is never hidden
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no SARIF message mentions %q:\n%s", want, all)
		}
	}
}

// TestSARIFLocations — a result without a location is one GitHub accepts and
// then fails to place, which makes the feature look broken rather than absent.
func TestSARIFLocations(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{Locations: sampleIndex()}))

	for _, res := range doc.Runs[0].Results {
		if len(res.Locations) != 1 {
			t.Fatalf("result %q has %d locations, want 1", res.Message.Text, len(res.Locations))
		}
		phys := res.Locations[0].PhysicalLocation
		if phys.ArtifactLocation.URI == "" || phys.Region.StartLine == 0 {
			t.Errorf("result %q has an empty location: %+v", res.Message.Text, phys)
		}
	}
}

// TestSARIFDropsUnlocatableLoudly is the rule that matters most here: a SARIF
// run with no results reads to GitHub as a clean bill of health, so anything we
// could not place has to be impossible to miss.
func TestSARIFDropsUnlocatableLoudly(t *testing.T) {
	var warned []string
	out := renderSARIF(t, sample(), report.WriteOptions{
		Locations: sampleIndex(), // aws_dynamodb_table.unmapped is absent from it
		Warn:      func(s string) { warned = append(warned, s) },
	})
	doc := decodeSARIF(t, out)
	run := doc.Runs[0]

	if strings.Contains(out, "aws_dynamodb_table.unmapped\"") {
		t.Error("an unlocatable finding was emitted as a result without a location")
	}
	if len(run.Invocations) != 1 || run.Invocations[0].ExecutionSuccessful {
		t.Error("executionSuccessful stayed true although results were dropped; " +
			"GitHub would show a clean analysis over an incomplete one")
	}

	var errorNote string
	for _, n := range run.Invocations[0].ToolExecutionNotifications {
		if n.Level == "error" {
			errorNote = n.Message.Text
		}
	}
	if !strings.Contains(errorNote, "aws_dynamodb_table.unmapped") {
		t.Errorf("the dropped finding was not named in a notification: %q", errorNote)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "aws_dynamodb_table.unmapped") {
		t.Errorf("the drop was not reported to the terminal: %v", warned)
	}
}

// A PARTIAL drop still produces a useful report, so it warns rather than fails.
// The asymmetry is the point: some annotations beat none, but zero annotations
// presented as a successful run is the failure this tool exists to prevent.
func TestSARIFPartialDropStillReports(t *testing.T) {
	var warned []string
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{
		Locations: sampleIndex(),
		Warn:      func(s string) { warned = append(warned, s) },
	}))
	run := doc.Runs[0]

	if len(run.Results) == 0 {
		t.Fatal("a partial drop produced no results at all")
	}
	if run.Invocations[0].ExecutionSuccessful {
		t.Error("an analysis that dropped a finding reported itself successful")
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "aws_dynamodb_table.unmapped") {
		t.Errorf("the drop was not reported to the terminal: %v", warned)
	}
}

// TestSARIFCarriesRunWarnings — a throttled batch means results may be missing,
// and that must reach GitHub, not just the terminal.
func TestSARIFCarriesRunWarnings(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{Locations: sampleIndex()}))

	var found bool
	for _, n := range doc.Runs[0].Invocations[0].ToolExecutionNotifications {
		if strings.Contains(n.Message.Text, "one batch was throttled") {
			found = true
		}
	}
	if !found {
		t.Error("a run-level warning did not reach the SARIF notifications")
	}
}

// TestSARIFEmptyReportIsClean — nothing to check is a legitimate green run, and
// must not be confused with an incomplete one.
func TestSARIFEmptyReportIsClean(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, &finding.Report{}, report.WriteOptions{Locations: sampleIndex()}))
	run := doc.Runs[0]

	if len(run.Results) != 0 {
		t.Errorf("got %d results for an empty report", len(run.Results))
	}
	if !run.Invocations[0].ExecutionSuccessful {
		t.Error("an empty plan reported an unsuccessful analysis")
	}
}

// TestSARIFRuleIndexMatchesRuleID — a mismatched ruleIndex silently attaches an
// alert to the wrong rule's description in the GitHub UI.
func TestSARIFRuleIndexMatchesRuleID(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{Locations: sampleIndex()}))
	rules := doc.Runs[0].Tool.Driver.Rules

	for _, res := range doc.Runs[0].Results {
		if res.RuleIndex < 0 || res.RuleIndex >= len(rules) {
			t.Fatalf("ruleIndex %d out of range for %d rules", res.RuleIndex, len(rules))
		}
		if rules[res.RuleIndex].ID != res.RuleID {
			t.Errorf("ruleIndex %d points at %q, but ruleId is %q",
				res.RuleIndex, rules[res.RuleIndex].ID, res.RuleID)
		}
	}
}

func TestSARIFToolVersionStripsTheVPrefix(t *testing.T) {
	doc := decodeSARIF(t, renderSARIF(t, sample(), report.WriteOptions{
		Locations:   sampleIndex(),
		ToolVersion: "v1.2.3",
	}))
	// SARIF semanticVersion is semver, which has no leading "v"; the tool's own
	// version string does.
	if got := doc.Runs[0].Tool.Driver.SemanticVersion; got != "1.2.3" {
		t.Errorf("semanticVersion = %q, want 1.2.3", got)
	}
}
