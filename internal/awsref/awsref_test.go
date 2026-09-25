package awsref_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Caleb-Kelly-25/preflight/internal/awsref"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
)

// reference is a cut-down service document in AWS's real shape. Captured rather
// than invented: the nesting of Actions[].Resources[].Name and
// Resources[].ARNFormats is exactly what
// servicereference.us-east-1.amazonaws.com returns, and the three actions below
// are the real ones that motivated this check.
const reference = `{
  "Name": "lambda",
  "Actions": [
    {"Name": "PublishLayerVersion", "Resources": [{"Name": "layer"}]},
    {"Name": "DeleteLayerVersion",  "Resources": [{"Name": "layerVersion"}]},
    {"Name": "GetLayerVersion",     "Resources": [{"Name": "layerVersion"}]},
    {"Name": "ListFunctions"}
  ],
  "Resources": [
    {"Name": "layer",        "ARNFormats": ["arn:${Partition}:lambda:${Region}:${Account}:layer:${LayerName}"]},
    {"Name": "layerVersion", "ARNFormats": ["arn:${Partition}:lambda:${Region}:${Account}:layer:${LayerName}:${LayerVersion}"]}
  ]
}`

func parse(t *testing.T) *awsref.Service {
	t.Helper()
	s, err := awsref.Parse(strings.NewReader(reference))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func TestResourceTypesFor(t *testing.T) {
	s := parse(t)

	types, scopeless, known := s.ResourceTypesFor("lambda:DeleteLayerVersion")
	if !known || scopeless || len(types) != 1 || types[0] != "layerVersion" {
		t.Errorf("DeleteLayerVersion = (%v, scopeless=%v, known=%v)", types, scopeless, known)
	}

	// An action with no Resources authorizes only against "*". That is a real
	// state, not a missing field, and the caller branches on it.
	if _, scopeless, known := s.ResourceTypesFor("lambda:ListFunctions"); !known || !scopeless {
		t.Errorf("ListFunctions should be known and scopeless, got scopeless=%v known=%v", scopeless, known)
	}

	// An unknown action is reported separately from a mismatch: a typo and a
	// wrong scope need different fixes.
	if _, _, known := s.ResourceTypesFor("lambda:NoSuchAction"); known {
		t.Error("an action absent from the reference was reported as known")
	}
}

// Matching is structural, so an entry naming its variables differently from AWS
// still matches. The shape carries the meaning; the variable names do not.
func TestResourceTypesMatchingIgnoresVariableNames(t *testing.T) {
	s := parse(t)
	got := s.ResourceTypesMatching("arn:${Partition}:lambda:${Region}:${Account}:layer:${Whatever}")
	if len(got) != 1 || got[0] != "layer" {
		t.Errorf("got %v, want [layer]", got)
	}
}

// The check exists to catch one thing above all: an entry scoped to an ARN
// BROADER than what the action authorizes against, which allows a query the
// real call would fail.
func TestBroaderScopeIsAnError(t *testing.T) {
	db := dbWith(t, `
resources:
  - type: aws_lambda_layer_version
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:layer:${LayerName}"
    arn_attributes: { LayerName: layer_name }
    operations:
      delete: [lambda:DeleteLayerVersion]
`)
	f := findFor(t, awsref.Check(db, fixedFetcher(t)), "lambda:DeleteLayerVersion")
	if f.Severity != awsref.Error {
		t.Errorf("severity = %s, want ERROR: a broader scope can report ALLOWED for a call that is denied", f.Severity)
	}
}

// The mirror case must NOT be an error. A narrower scope over-reports, which is
// expensive but visible — grading it the same as a false pass would make the
// output unreadable and train the reader to skim.
func TestNarrowerScopeIsOnlyAWarning(t *testing.T) {
	db := dbWith(t, `
resources:
  - type: aws_lambda_layer_version
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:layer:${LayerName}:${LayerVersion}"
    arn_attributes: { LayerName: layer_name, LayerVersion: version }
    operations:
      create: [lambda:PublishLayerVersion]
`)
	f := findFor(t, awsref.Check(db, fixedFetcher(t)), "lambda:PublishLayerVersion")
	if f.Severity != awsref.Warn {
		t.Errorf("severity = %s, want WARN: a narrower scope over-reports and fails safe", f.Severity)
	}
}

// A passthrough format is a variable holding a whole ARN. There is no shape to
// compare, and calling that a mismatch would be this tool's own false positive.
func TestPassthroughFormatIsNotAMismatch(t *testing.T) {
	db := dbWith(t, `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    arn_format: "${SomeArn}"
    arn_attributes: { SomeArn: arn }
    operations:
      create: [lambda:PublishLayerVersion]
`)
	for _, f := range awsref.Check(db, fixedFetcher(t)) {
		if f.Severity != awsref.Info {
			t.Errorf("passthrough produced %s: %s", f.Severity, f.Detail)
		}
	}
}

// An unreachable endpoint must not read as a clean result. The check reports it
// and declines to judge, rather than silently passing every entry in a service
// it could not load.
func TestUnreachableServiceDoesNotSilentlyPass(t *testing.T) {
	db := dbWith(t, `
resources:
  - type: aws_thing
    service: lambda
    status: draft
    arn_format: "arn:${Partition}:lambda:${Region}:${Account}:layer:${LayerName}"
    arn_attributes: { LayerName: name }
    operations:
      create: [lambda:PublishLayerVersion]
`)
	findings := awsref.Check(db, func(string) (*awsref.Service, error) {
		return nil, fmt.Errorf("simulated outage")
	})
	if len(findings) == 0 {
		t.Fatal("an unreachable service produced no findings at all")
	}
	if awsref.Worst(findings) != awsref.Info {
		t.Error("an outage was graded above Info; that reddens the build for someone else's problem")
	}
	if !strings.Contains(findings[0].Detail, "nothing in it was checked") {
		t.Errorf("the finding does not say the check did not run: %q", findings[0].Detail)
	}
}

func fixedFetcher(t *testing.T) awsref.Fetcher {
	t.Helper()
	return func(string) (*awsref.Service, error) { return awsref.Parse(strings.NewReader(reference)) }
}

func dbWith(t *testing.T, doc string) *mapping.Database {
	t.Helper()
	db, err := mapping.Load(fstest.MapFS{"t.yaml": &fstest.MapFile{Data: []byte(doc)}})
	if err != nil {
		t.Fatalf("loading test database: %v", err)
	}
	return db
}

func findFor(t *testing.T, findings []awsref.Finding, action string) awsref.Finding {
	t.Helper()
	for _, f := range findings {
		if f.Action == action {
			return f
		}
	}
	t.Fatalf("no finding for %s; got %v", action, findings)
	return awsref.Finding{}
}
