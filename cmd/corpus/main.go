// Command corpus measures what preflight could say about real Terraform plans.
//
// It answers docs/DESIGN.md §16 open question 3 — what fraction of resource
// changes reach an Exact ARN — and, as a side effect, which unmapped resource
// types real configurations actually contain. The second half is the more
// immediately useful one: it replaces guessing about what to map next with a
// frequency count.
//
// MAINTAINER TOOL. Not built by goreleaser (.goreleaser.yaml builds only
// ./cmd/preflight) and not part of the product surface.
//
// IT TOUCHES NOTHING. No AWS calls, no credentials, no network, no Terraform.
// Plan parsing, the mapping lookup and BuildARN are all pure functions, which is
// the entire reason this measurement is cheap enough to re-run whenever the
// database grows. A plan file is the only input.
//
// WHY THIS IS NOT `preflight check --dry-run`. The engine deliberately reports
// what it CAN conclude; this reports the CEILING — the best confidence a finding
// could reach if the simulator answered perfectly. Those are different questions,
// and folding the second into the product's output would invite reading a
// ceiling as a result.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/internal/plan"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

// The account and partition a real run gets from sts:GetCallerIdentity, and the
// region from the plan or the environment. They are supplied here as constants
// because BuildARN reports inexact when ANY placeholder is unresolved, including
// ${Account} and ${Region} — so leaving them empty would score every
// account-scoped ARN as inexact and make the headline number meaningless.
//
// This cannot bias the measurement upward: in a real run all three are always
// available, so supplying them reproduces the real conditions rather than
// improving on them. What the measurement is actually about is whether the
// plan carries the resource's own identifying attributes.
const (
	corpusPartition = "aws"
	corpusAccount   = "000000000000"
	corpusRegion    = "us-east-1"
)

// bucket is the best confidence a unit could reach, and why.
type bucket int

const (
	// bucketVerified: the operation was established empirically AND the ARN is
	// exact. Only these can ever reach Verified.
	bucketVerified bucket = iota
	// bucketDraftOp: mapped and exact, but the operation is not measured, so the
	// entry caps at Likely however well the simulation goes.
	bucketDraftOp
	// bucketInexactARN: mapped, but the ARN could not be pinned down, so a
	// denial is inconclusive and an allow cannot be trusted against an
	// ARN-scoped policy.
	bucketInexactARN
	// bucketUnmapped: the resource type is not in the database at all.
	bucketUnmapped
)

type counts struct {
	Units    int `json:"units"`
	Verified int `json:"verified_reachable"`
	DraftOp  int `json:"capped_by_draft_operation"`
	Inexact  int `json:"capped_by_inexact_arn"`
	Unmapped int `json:"unmapped_type"`

	// ARN resolution across MAPPED units only. An unmapped type has no ARN
	// template, so counting it as "wildcard" would blame the ARN machinery for a
	// coverage gap and overstate how often ARNs fail.
	ARNExact    int `json:"arn_exact"`
	ARNPrefix   int `json:"arn_prefix_derived"`
	ARNWildcard int `json:"arn_wildcard"`
}

func (c *counts) add(o *counts) {
	c.Units += o.Units
	c.Verified += o.Verified
	c.DraftOp += o.DraftOp
	c.Inexact += o.Inexact
	c.Unmapped += o.Unmapped
	c.ARNExact += o.ARNExact
	c.ARNPrefix += o.ARNPrefix
	c.ARNWildcard += o.ARNWildcard
}

type planResult struct {
	Path             string `json:"path"`
	TerraformVersion string `json:"terraform_version,omitempty"`
	Changes          int    `json:"actionable_changes"`
	counts
}

type report struct {
	Plans []planResult `json:"plans"`
	Total counts       `json:"total"`

	// ByOperation is not a nicety. ARN exactness is overwhelmingly a property of
	// the OPERATION: a create is the one case where the resource does not exist
	// yet, so its identifying attributes are unknown-until-apply, while an update
	// or a delete acts on something already in state whose name is known. A
	// single headline figure therefore says more about the create/update mix of
	// whatever plans were collected than about the tool, and any corpus drawn
	// from module examples is nearly all creates. Splitting it keeps that
	// confounder visible instead of averaged away.
	ByOperation map[string]*counts `json:"by_operation,omitempty"`
	// UnmappedTypes is the coverage-targeting output: which types appeared, how
	// often, and in how many distinct plans. Breadth matters more than raw
	// count — a type in one plan 40 times is one team's habit, a type in six
	// plans once each is a real gap.
	UnmappedTypes []typeCount `json:"unmapped_types,omitempty"`
	// WildcardTypes are mapped types that fell all the way to a bare "*". These
	// are the improvement targets: a wildcard denial proves nothing and is
	// suppressed, so these units lose the red-finding signal entirely.
	//
	// Kept SEPARATE from prefix-derived, which was originally lumped in with it.
	// That conflation was actively misleading: aws_iam_role looked like the
	// worst offender in the corpus, and 19 of its 30 units were resolving
	// through arn_prefix_attributes exactly as designed. A breakdown that mixes
	// "as good as it can get" with "no signal at all" points work at the wrong
	// entries.
	WildcardTypes []typeCount `json:"wildcard_arn_types,omitempty"`
	// PrefixTypes resolved through arn_prefix_attributes. Not exact, but a
	// prefix-scoped policy matches them, so they are working as intended.
	PrefixTypes []typeCount `json:"prefix_arn_types,omitempty"`
}

type typeCount struct {
	Type  string `json:"type"`
	Units int    `json:"units"`
	Plans int    `json:"plans"`
}

func main() {
	var (
		asJSON  = flag.Bool("json", false, "emit the full measurement as JSON")
		perPlan = flag.Bool("per-plan", false, "also print a line per plan file")
		topN    = flag.Int("top", 15, "how many types to list in each breakdown")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: corpus [flags] <plan.json | directory>...\n\n")
		fmt.Fprintf(os.Stderr, "Measures the ceiling preflight could reach on real plans.\n")
		fmt.Fprintf(os.Stderr, "Reads plan files only; makes no AWS calls.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	db, err := mapping.Load(mappings.FS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "corpus: loading the mapping database: %v\n", err)
		os.Exit(2)
	}

	files, err := collectPlanFiles(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "corpus: %v\n", err)
		os.Exit(2)
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "corpus: no .json files found in the given paths")
		os.Exit(2)
	}

	rep, err := measure(db, files)
	if err != nil {
		fmt.Fprintf(os.Stderr, "corpus: %v\n", err)
		os.Exit(2)
	}
	rep.limitBreakdowns(*topN)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(os.Stderr, "corpus: writing json: %v\n", err)
			os.Exit(2)
		}
		return
	}
	rep.writeText(os.Stdout, *perPlan)
}

// collectPlanFiles expands directories into the .json files inside them. A file
// named explicitly is taken whatever its extension, because a plan does not have
// to be called *.json; only directory walking filters, to avoid pulling in every
// unrelated json file a repository happens to contain.
func collectPlanFiles(args []string) ([]string, error) {
	var out []string
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", arg, err)
		}
		if !info.IsDir() {
			out = append(out, arg)
			continue
		}
		err = filepath.WalkDir(arg, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".json") {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking %s: %w", arg, err)
		}
	}
	sort.Strings(out)
	return out, nil
}

func measure(db *mapping.Database, files []string) (*report, error) {
	rep := &report{ByOperation: map[string]*counts{}}

	unmappedUnits := map[string]int{}
	unmappedPlans := map[string]int{}
	wildUnits := map[string]int{}
	wildPlans := map[string]int{}
	prefixUnits := map[string]int{}
	prefixPlans := map[string]int{}

	for _, path := range files {
		one, err := measureOne(db, path)
		if err != nil {
			// A directory of real-world plans will contain the occasional file
			// that is not a plan. Skipping it loudly is better than aborting the
			// sweep, and better than skipping it silently — a corpus that
			// quietly measured half of what was handed to it would produce a
			// confident number about the wrong population.
			fmt.Fprintf(os.Stderr, "corpus: skipping %s: %v\n", path, err)
			continue
		}
		rep.Plans = append(rep.Plans, one.plan)
		rep.Total.add(&one.plan.counts)

		for op, c := range one.byOp {
			if rep.ByOperation[op] == nil {
				rep.ByOperation[op] = &counts{}
			}
			rep.ByOperation[op].add(c)
		}
		for t, n := range one.unmapped {
			unmappedUnits[t] += n
			unmappedPlans[t]++
		}
		for t, n := range one.wildcard {
			wildUnits[t] += n
			wildPlans[t]++
		}
		for t, n := range one.prefixed {
			prefixUnits[t] += n
			prefixPlans[t]++
		}
	}

	rep.UnmappedTypes = rankTypes(unmappedUnits, unmappedPlans)
	rep.WildcardTypes = rankTypes(wildUnits, wildPlans)
	rep.PrefixTypes = rankTypes(prefixUnits, prefixPlans)
	return rep, nil
}

// oneResult is everything one plan file contributes. Grouped into a struct
// rather than returned as five values, because the two type-frequency maps are
// easy to transpose at a call site and the compiler would not notice.
type oneResult struct {
	plan     planResult
	byOp     map[string]*counts
	unmapped map[string]int
	wildcard map[string]int
	prefixed map[string]int
}

func measureOne(db *mapping.Database, path string) (*oneResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening plan file: %w", err)
	}
	defer f.Close()

	p, err := plan.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}

	// The plan's own provider region when it declares one, so that a plan
	// targeting eu-west-1 is measured in eu-west-1. Region almost never decides
	// exactness, but using the plan's value costs nothing and removes a question.
	arnCtx := mapping.ARNContext{
		Partition: corpusPartition,
		Account:   corpusAccount,
		Region:    corpusRegion,
	}
	if region, ok := p.ProviderRegion(); ok && region != "" {
		arnCtx.Region = region
	}

	out := &oneResult{
		plan:     planResult{Path: path, TerraformVersion: p.TerraformVersion},
		byOp:     map[string]*counts{},
		unmapped: map[string]int{},
		wildcard: map[string]int{},
		prefixed: map[string]int{},
	}
	pr := &out.plan

	changes := p.Actionable()
	pr.Changes = len(changes)

	for _, rc := range changes {
		for _, op := range rc.Change.Operations() {
			// Every tally lands in both the plan total and the per-operation
			// bucket. Kept as a slice so no counter can be updated in one place
			// and forgotten in the other — the failure that would make the
			// per-operation split disagree with the headline.
			if out.byOp[string(op)] == nil {
				out.byOp[string(op)] = &counts{}
			}
			into := []*counts{&pr.counts, out.byOp[string(op)]}
			bump := func(f func(*counts)) {
				for _, c := range into {
					f(c)
				}
			}

			bump(func(c *counts) { c.Units++ })

			res, ok := db.Lookup(rc.Type)
			if !ok {
				bump(func(c *counts) { c.Unmapped++ })
				out.unmapped[rc.Type]++
				continue
			}

			// Mirrors engine.prepare: deletes act on the prior state, creates
			// and updates on the planned state. If that rule ever changes in the
			// engine it must change here too, or the corpus will measure a
			// population the product does not.
			attrs, unknown := rc.Change.After, rc.Change.AfterUnknown
			if op == plan.ActionDelete {
				attrs, unknown = rc.Change.Before, nil
			}

			arn, exact := res.BuildARN(arnCtx, attrs, unknown)
			switch {
			case exact:
				bump(func(c *counts) { c.ARNExact++ })
			case arn == "*":
				bump(func(c *counts) { c.ARNWildcard++ })
				out.wildcard[rc.Type]++
			default:
				// A name_prefix-derived ARN. Not exact, so it caps at Likely,
				// but it matches a policy scoped with a trailing wildcard where
				// "*" matches nothing at all — worth counting separately.
				bump(func(c *counts) { c.ARNPrefix++ })
				out.prefixed[rc.Type]++
			}

			switch {
			case !exact:
				bump(func(c *counts) { c.Inexact++ })
			case !res.VerifiedFor(mapping.Operation(op)):
				bump(func(c *counts) { c.DraftOp++ })
			default:
				bump(func(c *counts) { c.Verified++ })
			}
		}
	}
	return out, nil
}

func rankTypes(units, plans map[string]int) []typeCount {
	out := make([]typeCount, 0, len(units))
	for t, n := range units {
		out = append(out, typeCount{Type: t, Units: n, Plans: plans[t]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plans != out[j].Plans {
			return out[i].Plans > out[j].Plans
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func (r *report) limitBreakdowns(n int) {
	if n <= 0 {
		return
	}
	if len(r.UnmappedTypes) > n {
		r.UnmappedTypes = r.UnmappedTypes[:n]
	}
	if len(r.WildcardTypes) > n {
		r.WildcardTypes = r.WildcardTypes[:n]
	}
	if len(r.PrefixTypes) > n {
		r.PrefixTypes = r.PrefixTypes[:n]
	}
}

func pct(n, total int) string {
	if total == 0 {
		return "   —  "
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(total))
}

func (r *report) writeText(w io.Writer, perPlan bool) {
	t := r.Total
	mapped := t.Units - t.Unmapped

	fmt.Fprintf(w, "%d plans, %d change×operation units\n\n", len(r.Plans), t.Units)

	if perPlan {
		fmt.Fprintf(w, "per plan\n")
		for _, p := range r.Plans {
			fmt.Fprintf(w, "  %-52s %4d units  %s exact\n",
				truncate(p.Path, 52), p.Units, pct(p.ARNExact, p.Units-p.Unmapped))
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "coverage\n")
	fmt.Fprintf(w, "  mapped                      %5d  %s\n", mapped, pct(mapped, t.Units))
	fmt.Fprintf(w, "  unmapped                    %5d  %s\n\n", t.Unmapped, pct(t.Unmapped, t.Units))

	fmt.Fprintf(w, "ARN resolution (mapped units only)\n")
	fmt.Fprintf(w, "  exact                       %5d  %s\n", t.ARNExact, pct(t.ARNExact, mapped))
	fmt.Fprintf(w, "  prefix-derived              %5d  %s\n", t.ARNPrefix, pct(t.ARNPrefix, mapped))
	fmt.Fprintf(w, "  wildcard \"*\"                %5d  %s\n\n", t.ARNWildcard, pct(t.ARNWildcard, mapped))

	fmt.Fprintf(w, "best reachable confidence\n")
	fmt.Fprintf(w, "  Verified                    %5d  %s   operation measured and ARN exact\n", t.Verified, pct(t.Verified, t.Units))
	fmt.Fprintf(w, "  Likely (draft operation)    %5d  %s   ARN exact, entry not measured for this op\n", t.DraftOp, pct(t.DraftOp, t.Units))
	fmt.Fprintf(w, "  Likely (inexact ARN)        %5d  %s   mapped, ARN not pinned down\n", t.Inexact, pct(t.Inexact, t.Units))
	fmt.Fprintf(w, "  Unchecked                   %5d  %s   resource type not mapped\n", t.Unmapped, pct(t.Unmapped, t.Units))

	// The split that keeps the headline honest: exactness is mostly a property of
	// the operation, so a corpus dominated by creates reports a low exact rate
	// about creates, not about preflight.
	if len(r.ByOperation) > 0 {
		fmt.Fprintf(w, "\nby operation\n")
		fmt.Fprintf(w, "  %-10s %6s  %7s  %7s  %7s\n", "operation", "units", "mapped", "exact", "Verified")
		for _, op := range []string{"create", "update", "delete"} {
			c, ok := r.ByOperation[op]
			if !ok {
				continue
			}
			m := c.Units - c.Unmapped
			fmt.Fprintf(w, "  %-10s %6d  %s  %s  %s\n",
				op, c.Units, pct(m, c.Units), pct(c.ARNExact, m), pct(c.Verified, c.Units))
		}
	}

	writeTypeTable(w, "\nunmapped types (by how many plans contain them)", r.UnmappedTypes)
	writeTypeTable(w, "\nmapped types that fell to a bare \"*\" (no denial signal at all)", r.WildcardTypes)
	writeTypeTable(w, "\nmapped types resolved via name_prefix (working as designed)", r.PrefixTypes)
}

func writeTypeTable(w io.Writer, title string, rows []typeCount) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(w, "%s\n", title)
	for _, row := range rows {
		fmt.Fprintf(w, "  %3d plans  %4d units  %s\n", row.Plans, row.Units, row.Type)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-(n-3):]
}
