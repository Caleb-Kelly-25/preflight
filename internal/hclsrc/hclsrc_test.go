package hclsrc_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/hclsrc"
)

func load(t *testing.T, dir string) *hclsrc.Index {
	t.Helper()
	ix, err := hclsrc.Load(dir)
	if err != nil {
		t.Fatalf("Load(%q): %v", dir, err)
	}
	return ix
}

func wantLocation(t *testing.T, ix *hclsrc.Index, address, file string, line int) {
	t.Helper()
	loc, ok := ix.Find(address)
	if !ok {
		t.Errorf("%s: not indexed", address)
		return
	}
	if loc.File != file || loc.Line != line {
		t.Errorf("%s: got %s:%d, want %s:%d", address, loc.File, loc.Line, file, line)
	}
}

// TestIndexesRootAndLocalModules covers the whole walk at once: plain
// resources, a JSON configuration file, a local module, a nested local module,
// and the same module called twice under different prefixes.
func TestIndexesRootAndLocalModules(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "config"))

	root := "testdata/config"
	wantLocation(t, ix, "aws_iam_role.deploy", root+"/main.tf", 4)
	wantLocation(t, ix, "aws_s3_bucket.logs", root+"/main.tf", 10)
	wantLocation(t, ix, "aws_s3_bucket.by_region", root+"/main.tf", 17)

	wantLocation(t, ix, "module.logging.aws_cloudwatch_log_group.app",
		root+"/modules/logging/main.tf", 1)
	wantLocation(t, ix, "module.logging.module.inner.aws_s3_bucket.archive",
		root+"/modules/logging/inner/main.tf", 1)

	// The second call to the same directory must be indexed too, or half a
	// configuration silently loses its annotations.
	wantLocation(t, ix, "module.fleet.aws_cloudwatch_log_group.app",
		root+"/modules/logging/main.tf", 1)
}

// TestCountAndForEachMapBackToTheBlock is the complication DESIGN §10.3 names
// first: one block, many addresses, one line.
func TestCountAndForEachMapBackToTheBlock(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "config"))
	root := "testdata/config"

	for _, address := range []string{
		"aws_s3_bucket.logs[0]",
		"aws_s3_bucket.logs[2]",
	} {
		wantLocation(t, ix, address, root+"/main.tf", 10)
	}

	for _, address := range []string{
		`aws_s3_bucket.by_region["eu-west-1"]`,
		// A key containing a dot and a bracket: splitting the address on
		// punctuation rather than scanning it would lose this one.
		`aws_s3_bucket.by_region["a.b[0]"]`,
	} {
		wantLocation(t, ix, address, root+"/main.tf", 17)
	}

	// A module can itself be expanded, so both halves of the address need
	// stripping.
	wantLocation(t, ix, "module.fleet[1].aws_cloudwatch_log_group.app",
		root+"/modules/logging/main.tf", 1)
}

func TestIndexesJSONConfiguration(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "config"))
	if _, ok := ix.Find("aws_vpc.main"); !ok {
		t.Error("aws_vpc.main from network.tf.json was not indexed")
	}
}

// TestSkipsWhatItMustNotIndex guards three exclusions that each move an
// annotation somewhere wrong if they regress.
func TestSkipsWhatItMustNotIndex(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "config"))

	// Data sources never produce a finding.
	if _, ok := ix.Find("data.aws_caller_identity.current"); ok {
		t.Error("a data source was indexed")
	}
	// Override files patch an existing block; main.tf holds the declaration.
	wantLocation(t, ix, "aws_iam_role.deploy", "testdata/config/main.tf", 4)

	// Registry modules live under .terraform/, which is not in version
	// control, so an annotation there could not be placed anyway.
	if _, ok := ix.Find("module.vpc.aws_vpc.this"); ok {
		t.Error("a registry module's resources were indexed")
	}
}

// TestNamesWhatItCouldNotIndex — a silently incomplete index is how annotations
// go missing without anyone noticing.
func TestNamesWhatItCouldNotIndex(t *testing.T) {
	warnings := strings.Join(load(t, filepath.Join("testdata", "config")).Warnings(), "\n")

	if !strings.Contains(warnings, "module.vpc") {
		t.Errorf("the registry module was not reported as unindexed:\n%s", warnings)
	}
	if !strings.Contains(warnings, "module.dynamic") {
		t.Errorf("the variable-sourced module was not reported as unindexed:\n%s", warnings)
	}
}

// TestUnparseableFileDegrades — one bad file must cost its own resources their
// locations and nothing more.
func TestUnparseableFileDegrades(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "broken"))

	if _, ok := ix.Find("aws_s3_bucket.good"); !ok {
		t.Error("a parse failure in one file lost the resources in another")
	}
	warnings := strings.Join(ix.Warnings(), "\n")
	if !strings.Contains(warnings, "unparseable.tf") {
		t.Errorf("the parse failure was not reported:\n%s", warnings)
	}
}

// TestModuleCycleTerminates — Terraform rejects a self-referential module, but
// the index has to terminate rather than trust that it never happens.
func TestModuleCycleTerminates(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "cycle"))

	if _, ok := ix.Find("aws_s3_bucket.root"); !ok {
		t.Error("the root module was not indexed")
	}
	if !strings.Contains(strings.Join(ix.Warnings(), "\n"), "cycle") {
		t.Errorf("the module cycle was not reported:\n%v", ix.Warnings())
	}
}

func TestMissingDirectoryIsAnError(t *testing.T) {
	if _, err := hclsrc.Load(filepath.Join("testdata", "does-not-exist")); err == nil {
		t.Error("Load accepted a directory that does not exist")
	}
}

// TestLookupSatisfiesReportInterface pins the primitive-returning shape that
// keeps internal/report free of the HCL dependency.
func TestLookupSatisfiesReportInterface(t *testing.T) {
	ix := load(t, filepath.Join("testdata", "config"))

	file, line, ok := ix.Lookup("aws_iam_role.deploy")
	if !ok || file != "testdata/config/main.tf" || line != 4 {
		t.Errorf("Lookup = %q, %d, %v", file, line, ok)
	}
	if _, _, ok := ix.Lookup("aws_s3_bucket.absent"); ok {
		t.Error("Lookup found an address that is not in the configuration")
	}
}

func TestBaseAddress(t *testing.T) {
	cases := map[string]string{
		"aws_s3_bucket.logs":                            "aws_s3_bucket.logs",
		"aws_s3_bucket.logs[0]":                         "aws_s3_bucket.logs",
		`aws_s3_bucket.logs["eu"]`:                      "aws_s3_bucket.logs",
		`aws_s3_bucket.logs["a.b"]`:                     "aws_s3_bucket.logs",
		`aws_s3_bucket.logs["a\"b"]`:                    "aws_s3_bucket.logs",
		`aws_s3_bucket.logs["]"]`:                       "aws_s3_bucket.logs",
		"module.env[0].aws_s3_bucket.logs[1]":           "module.env.aws_s3_bucket.logs",
		`module.env["a"].module.b["c"].aws_vpc.this[0]`: "module.env.module.b.aws_vpc.this",
	}
	for in, want := range cases {
		if got := hclsrc.BaseAddress(in); got != want {
			t.Errorf("BaseAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
