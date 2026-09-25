// Package hclsrc builds an index from Terraform resource address to the source
// file and line the resource is declared on.
//
// It exists for one reason: SARIF is only useful to GitHub if every result
// carries a file and a line, and the plan JSON carries no source positions at
// all. It identifies resources by address ("aws_s3_bucket.logs") and nothing
// else. The positions therefore have to come from a second pass over the
// configuration, which is what this package is.
//
// This is the only package that imports hashicorp/hcl. Nothing in the engine or
// the output layer depends on it, so an HCL parse failure can never influence a
// permission decision — the worst it can do is cost an annotation its location,
// and internal/report is explicit about that having happened.
package hclsrc

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
)

// Location is where a resource block is declared.
type Location struct {
	// File is a relative, slash-separated path, because that is what a SARIF
	// artifactLocation.uri has to be for GitHub to place the annotation against
	// a file in the pull request.
	//
	// A relative Load directory is kept as a prefix, so Load("infra") yields
	// "infra/main.tf" — a path relative to the working directory, which in CI
	// is the repository root. An absolute Load directory is dropped instead,
	// because an absolute path from a runner cannot be placed against anything,
	// and a path relative to the configuration root is the best remaining
	// guess.
	File string
	// Line is the line of the block header — the `resource "aws_s3_bucket"
	// "logs" {` line, not the body. That is the line a reviewer needs to look
	// at, and it is stable against edits inside the block.
	//
	// Deliberately no column: the annotation should cover the whole
	// declaration line, and a column would narrow it to the word "resource".
	Line int
}

// Index maps resource addresses to source locations.
type Index struct {
	byAddress map[string]Location
	warnings  []string
	// remote records module calls whose source is not a local path, so the
	// reason their resources have no location can be stated rather than guessed
	// at. See loadDir for why they are deliberately not followed.
	remote []string
}

// maxModuleDepth bounds module recursion. Terraform has no such limit, but a
// configuration nested this deeply is far likelier to be a symlink loop than a
// real design, and an index that never terminates is worse than a shallow one.
const maxModuleDepth = 32

// Load walks the Terraform configuration rooted at dir and indexes every
// managed resource block it can reach through local module calls.
//
// It returns an error only when dir itself cannot be read. Everything else —
// an unparseable file, a module whose source is a variable, a cycle — is
// recorded as a warning and skipped, because a configuration preflight cannot
// fully read is a reason to lose annotations, not a reason to fail the run.
func Load(dir string) (*Index, error) {
	if dir == "" {
		dir = "."
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("reading terraform configuration directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("reading terraform configuration directory: %s is not a directory", dir)
	}

	ix := &Index{byAddress: map[string]Location{}}
	ix.loadDir(dir, displayRoot(dir), "", 0, map[string]bool{})
	return ix, nil
}

// displayRoot is the prefix Location.File carries. See Location.File for why an
// absolute directory contributes nothing.
func displayRoot(dir string) string {
	if filepath.IsAbs(dir) {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(dir))
	if clean == "." {
		return ""
	}
	return clean
}

// Len reports how many addresses were indexed.
func (ix *Index) Len() int { return len(ix.byAddress) }

// Warnings returns everything that was skipped, in a stable order. The caller
// is expected to surface these: a silently incomplete index is how annotations
// go missing without anyone noticing.
func (ix *Index) Warnings() []string {
	out := append([]string(nil), ix.warnings...)
	if len(ix.remote) > 0 {
		mods := append([]string(nil), ix.remote...)
		sort.Strings(mods)
		out = append(out, fmt.Sprintf(
			"module(s) %s are not local paths, so resources inside them have no source location",
			strings.Join(mods, ", ")))
	}
	return out
}

// Find returns the location of the block declaring address.
func (ix *Index) Find(address string) (Location, bool) {
	loc, ok := ix.byAddress[BaseAddress(address)]
	return loc, ok
}

// Lookup satisfies report.SourceIndex.
//
// The interface is defined in terms of primitives rather than a shared struct
// so that internal/report need not import this package. That is what keeps the
// HCL dependency confined here instead of leaking into the output layer and,
// through it, into everything that renders a report.
func (ix *Index) Lookup(address string) (file string, line int, ok bool) {
	loc, found := ix.Find(address)
	return loc.File, loc.Line, found
}

// configSchema is the only part of the Terraform language this package reads.
// Data sources are deliberately absent: a data source is a read, and the engine
// never produces a finding for one.
var configSchema = &hcl.BodySchema{
	Blocks: []hcl.BlockHeaderSchema{
		{Type: "resource", LabelNames: []string{"type", "name"}},
		{Type: "module", LabelNames: []string{"name"}},
	},
}

var moduleSourceSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{{Name: "source"}},
}

// loadDir indexes one module directory.
//
// dir is the path on disk; display is the slash-separated prefix that goes into
// Location.File; prefix is the address prefix ("" at the root, "module.vpc."
// one level in); stack holds the absolute directories currently being walked,
// which is how a module that reaches itself through a relative path is caught.
func (ix *Index) loadDir(dir, display, prefix string, depth int, stack map[string]bool) {
	if depth > maxModuleDepth {
		ix.warnf("module nesting below %s exceeds %d levels; not indexed", addrOrRoot(prefix), maxModuleDepth)
		return
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if stack[abs] {
		ix.warnf("module %s forms a source cycle; not indexed", addrOrRoot(prefix))
		return
	}
	stack[abs] = true
	defer delete(stack, abs)

	entries, err := os.ReadDir(dir)
	if err != nil {
		ix.warnf("reading %s: %v", displayOrRoot(display), err)
		return
	}

	parser := hclparse.NewParser()
	for _, e := range entries {
		if e.IsDir() || !isConfigFile(e.Name()) {
			continue
		}
		file := filepath.Join(dir, e.Name())
		displayPath := path.Join(display, e.Name())

		var f *hcl.File
		var diags hcl.Diagnostics
		if strings.HasSuffix(e.Name(), ".json") {
			f, diags = parser.ParseJSONFile(file)
		} else {
			f, diags = parser.ParseHCLFile(file)
		}
		if diags.HasErrors() {
			// A file preflight cannot parse is a file whose resources get no
			// annotation. Terraform itself would reject it, so this most often
			// means the index was built against a different revision than the
			// plan — worth saying, never worth failing over.
			ix.warnf("parsing %s: %s", displayPath, firstDiag(diags))
		}
		if f == nil || f.Body == nil {
			continue
		}
		ix.indexBody(f.Body, dir, display, displayPath, prefix, depth, stack)
	}
}

func (ix *Index) indexBody(body hcl.Body, dir, display, displayPath, prefix string, depth int, stack map[string]bool) {
	content, _, diags := body.PartialContent(configSchema)
	if diags.HasErrors() {
		// PartialContent still returns the blocks it did understand, so this is
		// a note about what was lost, not a reason to drop the file.
		ix.warnf("reading blocks in %s: %s", displayPath, firstDiag(diags))
	}
	if content == nil {
		return
	}

	for _, block := range content.Blocks {
		switch block.Type {
		case "resource":
			if len(block.Labels) != 2 {
				continue
			}
			address := prefix + block.Labels[0] + "." + block.Labels[1]
			rng := block.DefRange
			if _, exists := ix.byAddress[address]; exists {
				// Two blocks claiming one address is invalid Terraform, so
				// rather than pick a line at random we keep the first and say
				// the second exists.
				ix.warnf("%s is declared more than once; using the first declaration", address)
				continue
			}
			ix.byAddress[address] = Location{File: displayPath, Line: rng.Start.Line}

		case "module":
			if len(block.Labels) != 1 {
				continue
			}
			ix.descend(block, dir, display, displayPath, prefix, depth, stack)
		}
	}
}

// descend follows a module call into its directory.
func (ix *Index) descend(block *hcl.Block, dir, display, displayPath, prefix string, depth int, stack map[string]bool) {
	callAddress := prefix + "module." + block.Labels[0]

	source, ok := moduleSource(block)
	if !ok {
		ix.warnf("module %s has no literal source in %s; resources inside it have no source location",
			callAddress, displayPath)
		return
	}
	if !isLocalSource(source) {
		// Registry and git modules land under .terraform/modules after
		// `terraform init`, so they could in principle be followed. They are
		// not, on purpose: that directory is not in version control, so an
		// annotation pointing into it names a file the pull request does not
		// contain and GitHub cannot place it. A missing annotation is the
		// honest outcome.
		ix.remote = append(ix.remote, callAddress)
		return
	}

	ix.loadDir(
		filepath.Join(dir, filepath.FromSlash(source)),
		path.Join(display, source),
		callAddress+".",
		depth+1,
		stack,
	)
}

// moduleSource extracts a module block's source when it is a literal string.
// A source built from a variable cannot be resolved without evaluating the
// configuration, which needs variable values we do not have.
func moduleSource(block *hcl.Block) (string, bool) {
	content, _, _ := block.Body.PartialContent(moduleSourceSchema)
	if content == nil {
		return "", false
	}
	attr, ok := content.Attributes["source"]
	if !ok {
		return "", false
	}
	val, diags := attr.Expr.Value(nil)
	if diags.HasErrors() || val.IsNull() || !val.IsKnown() || val.Type() != cty.String {
		return "", false
	}
	s := val.AsString()
	if s == "" {
		return "", false
	}
	return s, true
}

// isLocalSource reports whether a module source is a path on disk next to the
// calling configuration. Terraform's rule is exactly this prefix test: anything
// else is a registry address, a git URL, or an archive.
func isLocalSource(source string) bool {
	return strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") ||
		strings.HasPrefix(source, `.\`) || strings.HasPrefix(source, `..\`)
}

// isConfigFile applies Terraform's own file-selection rules, including the ones
// that exist to keep editor droppings out of a configuration.
func isConfigFile(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
		return false
	}
	if !strings.HasSuffix(name, ".tf") && !strings.HasSuffix(name, ".tf.json") {
		return false
	}
	// Override files merge into a block declared elsewhere; they do not
	// introduce an address of their own. Indexing them would move an annotation
	// off the real declaration and onto the patch.
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".tf")
	return base != "override" && !strings.HasSuffix(base, "_override")
}

// BaseAddress strips instance keys from a resource address, so that every
// instance produced by count or for_each maps back to the one block that
// declared them.
//
// "aws_s3_bucket.logs[0]" and `aws_s3_bucket.logs["eu"]` both reduce to
// "aws_s3_bucket.logs", and module calls are handled the same way, because a
// module can itself be expanded: "module.env[0].aws_s3_bucket.logs[1]" reduces
// to "module.env.aws_s3_bucket.logs". Quoted keys may contain brackets and dots
// of their own, which is why this scans rather than splitting on punctuation.
func BaseAddress(address string) string {
	if !strings.ContainsRune(address, '[') {
		return address
	}
	var b strings.Builder
	b.Grow(len(address))
	var depth int
	var inQuote bool
	for i := 0; i < len(address); i++ {
		c := address[i]
		switch {
		case inQuote:
			if c == '\\' && i+1 < len(address) {
				i++
				continue
			}
			if c == '"' {
				inQuote = false
			}
		case depth > 0 && c == '"':
			inQuote = true
		case c == '[':
			depth++
		case c == ']':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (ix *Index) warnf(format string, args ...any) {
	ix.warnings = append(ix.warnings, fmt.Sprintf(format, args...))
}

func firstDiag(diags hcl.Diagnostics) string {
	for _, d := range diags {
		if d.Severity == hcl.DiagError {
			return d.Error()
		}
	}
	return diags.Error()
}

func addrOrRoot(prefix string) string {
	if prefix == "" {
		return "the root module"
	}
	return strings.TrimSuffix(prefix, ".")
}

func displayOrRoot(display string) string {
	if display == "" {
		return "the configuration directory"
	}
	return display
}
