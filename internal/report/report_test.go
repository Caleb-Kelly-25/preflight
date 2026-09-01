package report_test

import (
	"encoding/json"
	"errors"
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
		},
	}
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

func TestSARIFStillReportsWhyItIsUnimplemented(t *testing.T) {
	var b strings.Builder
	err := report.Write(&b, sample(), report.FormatSARIF, report.WriteOptions{})
	if !errors.Is(err, report.ErrSARIFUnimplemented) {
		t.Errorf("err = %v, want ErrSARIFUnimplemented", err)
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
