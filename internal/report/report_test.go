package report_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/report"
)

// sample covers every rendering path at once: a conclusive denial, a suppressed
// one, a Likely with named unsupplied keys, and an unmapped type.
func sample() *finding.Report {
	return &finding.Report{
		PrincipalARN: "arn:aws:iam::123456789012:role/deploy",
		AccountID:    "123456789012",
		Warnings:     []string{"one batch was throttled"},
		Findings: []finding.Finding{
			{
				ResourceAddress: "aws_s3_bucket.denied",
				ResourceType:    "aws_s3_bucket",
				Operation:       "create",
				Level:           finding.LevelLikely,
				Reasons:         []finding.Reason{finding.ReasonResourcePolicyNotEvaluated},
				SimulatedARN:    "arn:aws:s3:::denied",
				Actions: []finding.ActionResult{
					{Action: "s3:CreateBucket", ResourceARN: "arn:aws:s3:::denied", Decision: finding.DecisionImplicitDeny},
				},
			},
			{
				ResourceAddress: "aws_s3_bucket.wildcard",
				ResourceType:    "aws_s3_bucket",
				Operation:       "create",
				Level:           finding.LevelUnchecked,
				Reasons:         []finding.Reason{finding.ReasonARNUnresolved},
				SimulatedARN:    "*",
				Actions: []finding.ActionResult{
					{
						Action: "s3:CreateBucket", ResourceARN: "*",
						Decision:           finding.DecisionImplicitDeny,
						Inconclusive:       true,
						InconclusiveReason: finding.ReasonARNUnresolved,
					},
				},
			},
			{
				ResourceAddress:       "aws_iam_role.gated",
				ResourceType:          "aws_iam_role",
				Operation:             "create",
				Level:                 finding.LevelLikely,
				Reasons:               []finding.Reason{finding.ReasonConditionKeysUnknown},
				UnsuppliedContextKeys: []string{"aws:SourceIp"},
				SuppliedContext: []finding.ContextEntry{
					{Key: "aws:RequestedRegion", Type: finding.ContextString, Values: []string{"us-east-1"}},
				},
				Actions: []finding.ActionResult{
					{Action: "iam:CreateRole", ResourceARN: "arn:aws:iam::1:role/g", Decision: finding.DecisionAllowed},
				},
			},
			{
				ResourceAddress: "aws_dynamodb_table.unmapped",
				ResourceType:    "aws_dynamodb_table",
				Operation:       "update",
				Level:           finding.LevelUnchecked,
				Reasons:         []finding.Reason{finding.ReasonNoMapping},
			},
			{
				ResourceAddress: "aws_iam_role.clean",
				ResourceType:    "aws_iam_role",
				Operation:       "create",
				Level:           finding.LevelVerified,
				SimulatedARN:    "arn:aws:iam::123456789012:role/clean",
				Actions: []finding.ActionResult{
					{Action: "iam:CreateRole", ResourceARN: "arn:aws:iam::123456789012:role/clean", Decision: finding.DecisionAllowed},
				},
			},
		},
	}
}

// golden compares against a checked-in file, or rewrites it under -update.
//
// The three output formats are contracts with three different readers — a
// human, a dashboard, and GitHub — and a diff against a golden file is the only
// review that shows all of what changed rather than what a test happened to
// assert. DESIGN §13.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	// Rendered output is compared line-wise, so a checkout that normalised line
	// endings must not read as a failure.
	got = strings.ReplaceAll(got, "\r\n", "\n")

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("updating golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run go test ./internal/report -update)", path, err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("%s does not match the golden file; run go test ./internal/report -update to see the diff\n--- got ---\n%s", name, got)
	}
}

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func TestTextGolden(t *testing.T) {
	golden(t, "report.txt", renderText(t, sample(), report.WriteOptions{}))
}

func TestTextExplainGolden(t *testing.T) {
	golden(t, "report-explain.txt", renderText(t, sample(), report.WriteOptions{Explain: true}))
}

func TestJSONGolden(t *testing.T) {
	var b strings.Builder
	if err := report.WriteJSON(&b, sample()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	golden(t, "report.json", b.String())
}

func renderText(t *testing.T, r *finding.Report, opts report.WriteOptions) string {
	t.Helper()
	var b strings.Builder
	if err := report.WriteText(&b, r, opts); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	return b.String()
}

func TestTextReportSeparatesConclusiveFromSuppressed(t *testing.T) {
	out := renderText(t, sample(), report.WriteOptions{})

	// The conclusive denial is actionable and must be named.
	if !strings.Contains(out, "MISSING PERMISSIONS (1)") {
		t.Errorf("want exactly one missing-permission finding:\n%s", out)
	}
	if !strings.Contains(out, "missing: s3:CreateBucket") {
		t.Errorf("the conclusive denial did not name its action:\n%s", out)
	}

	// The suppressed one must not appear there, but must be explained — a user
	// who knows AWS said no should never see preflight silently say nothing.
	if strings.Contains(out, "aws_s3_bucket.wildcard  create") &&
		strings.Index(out, "aws_s3_bucket.wildcard") < strings.Index(out, "UNCHECKED") {
		t.Errorf("the suppressed denial was listed as a missing permission:\n%s", out)
	}
	if !strings.Contains(out, "NOTE: 1 denial(s) were not reported") {
		t.Errorf("suppressed denials were not explained:\n%s", out)
	}
}

// TestTextReportNamesUnsuppliedKeys — a reason the reader cannot act on is a
// reason they learn to ignore.
func TestTextReportNamesUnsuppliedKeys(t *testing.T) {
	out := renderText(t, sample(), report.WriteOptions{})
	if !strings.Contains(out, "aws:SourceIp") {
		t.Errorf("the unsupplied condition key was not named:\n%s", out)
	}
}

func TestExplainShowsContextAndRawDecisions(t *testing.T) {
	plain := renderText(t, sample(), report.WriteOptions{})
	explained := renderText(t, sample(), report.WriteOptions{Explain: true})

	if strings.Contains(plain, "scoped to:") {
		t.Error("explain-only detail leaked into the default output")
	}
	for _, want := range []string{
		"scoped to: arn:aws:s3:::denied",
		"context: aws:RequestedRegion = us-east-1",
		"context: aws:SourceIp = (could not supply)",
		"not acted on:",
	} {
		if !strings.Contains(explained, want) {
			t.Errorf("--explain output missing %q:\n%s", want, explained)
		}
	}
}

func TestEmptyReport(t *testing.T) {
	out := renderText(t, &finding.Report{}, report.WriteOptions{})
	if !strings.Contains(out, "No AWS resource changes") {
		t.Errorf("empty report rendered oddly:\n%s", out)
	}
}

// TestJSONSchemaVersion — teams build dashboards on this output, and they are
// exactly the users we least want to break.
func TestJSONSchemaVersion(t *testing.T) {
	var b strings.Builder
	if err := report.WriteJSON(&b, sample()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var doc struct {
		SchemaVersion string `json:"schema_version"`
		PrincipalARN  string `json:"principal_arn"`
		Findings      []struct {
			Address string `json:"resource_address"`
			Level   string `json:"level"`
			Actions []struct {
				Decision     string `json:"decision"`
				Inconclusive bool   `json:"inconclusive"`
			} `json:"actions"`
		} `json:"findings"`
		Summary struct {
			Verified, Likely, Unchecked, Denied int
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(b.String()), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}

	if doc.SchemaVersion != report.SchemaVersion {
		t.Errorf("schema_version = %q, want %q", doc.SchemaVersion, report.SchemaVersion)
	}
	if doc.Summary.Denied != 1 {
		t.Errorf("summary.denied = %d, want 1 — the suppressed denial must not be counted",
			doc.Summary.Denied)
	}
	// The raw decision stays visible even when suppressed; consumers can see
	// what AWS actually said.
	if doc.Findings[1].Actions[0].Decision != "implicitDeny" || !doc.Findings[1].Actions[0].Inconclusive {
		t.Errorf("suppressed denial lost its raw decision: %+v", doc.Findings[1].Actions[0])
	}
}

func TestParseFormat(t *testing.T) {
	for _, ok := range []string{"text", "json", "sarif"} {
		if _, err := report.ParseFormat(ok); err != nil {
			t.Errorf("ParseFormat(%q): %v", ok, err)
		}
	}
	if _, err := report.ParseFormat("yaml"); err == nil {
		t.Error("ParseFormat accepted an unknown format")
	}
}

// TestStatsReachTheUser guards plan item 0.4, which was an unfulfilled promise for
// the whole of M2: internal/engine/simulator.go documented Stats as "surfaced under
// --explain", and engine.Options.resolve copied Warnings and Outcomes and dropped
// them. The numbers were computed on every run and discarded.
//
// That mattered beyond tidiness. IAM's simulate throttling limits are unpublished,
// and the only way to learn them is from real runs in the field — which requires the
// numbers to reach somebody.
func TestStatsReachTheUser(t *testing.T) {
	rep := &finding.Report{
		PrincipalARN: "arn:aws:iam::123456789012:user/deployer",
		Findings: []finding.Finding{{
			ResourceAddress: "aws_s3_bucket.b",
			Operation:       "create",
			Level:           finding.LevelVerified,
		}},
		Stats: &finding.SimulationStats{
			Calls: 3, Evaluations: 42, Pages: 2, Retries: 1, Throttles: 1,
			CacheHits: 7, ElapsedMS: 1234,
		},
	}

	var withExplain strings.Builder
	if err := report.WriteText(&withExplain, rep, report.WriteOptions{Explain: true}); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	got := withExplain.String()

	for _, want := range []string{
		"simulation",
		"3 API call(s)",
		"42 evaluation(s)",
		"14.0 per call", // the ratio: 42/3, which is how batching is measured
		"2 pages",
		"7 cache hit(s)",
		"1 throttle(s), 1 retry(ies)",
		"1234ms",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("--explain output is missing %q:\n%s", want, got)
		}
	}

	// Without --explain it must stay out of the way. This block answers a
	// maintainer's question, and a team reading a failing check does not need to
	// scroll past cache-hit counts to find out whether their deploy will work.
	var plain strings.Builder
	if err := report.WriteText(&plain, rep, report.WriteOptions{}); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(plain.String(), "API call(s)") {
		t.Errorf("stats leaked into the default output:\n%s", plain.String())
	}
}

// TestNoStatsWhenNothingWasSimulated pins the nil case. A report with no simulation
// — every finding decided without asking AWS, or the call failed outright — must
// print nothing rather than a row of zeros, which would read as "we asked AWS and it
// returned nothing". That is a different and more alarming claim than "we never
// asked".
func TestNoStatsWhenNothingWasSimulated(t *testing.T) {
	rep := &finding.Report{
		PrincipalARN: "arn:aws:iam::123456789012:user/deployer",
		Findings: []finding.Finding{{
			ResourceAddress: "aws_thing.x",
			Operation:       "create",
			Level:           finding.LevelUnchecked,
		}},
	}
	var b strings.Builder
	if err := report.WriteText(&b, rep, report.WriteOptions{Explain: true}); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if strings.Contains(b.String(), "simulation\n") || strings.Contains(b.String(), "API call(s)") {
		t.Errorf("printed simulation stats when none were recorded:\n%s", b.String())
	}
}
