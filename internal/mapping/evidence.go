package mapping

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Evidence is the recorded proof behind one entry's verification claims.
//
// WHY THIS EXISTS. `status: verified` and `verified_operations` are the strongest
// claims the database makes: they are what lets a finding reach `Verified`
// instead of being capped at `Likely`. Until this file existed, every one of
// those claims was backed only by prose in the entry's `notes` field, which meant
// two things nobody could see:
//
//   - a contributor could type `verified_operations: [create]` with no
//     measurement behind it at all, and CI would pass;
//   - someone could DELETE a measured action from a verified entry and nothing
//     would fail — the false-pass direction, on the entries carrying the
//     strongest claim.
//
// An evidence file is a transcript of what a derivation run actually measured. It
// is not a claim; the claim stays in the YAML, where a person makes it and
// justifies it in a pull request. This is the record that makes the claim
// auditable afterwards.
//
// Evidence files live in `mappings/evidence/<resource_type>.json` and are
// deliberately NOT embedded in the binary: they are a maintainer and CI artifact,
// not something the tool reads at runtime.
type Evidence struct {
	ResourceType string `json:"resource_type"`
	// Runs is every derivation run recorded for this type. One operation can
	// have several, and for an update it usually must: an update path depends on
	// which attribute changed, so each branch needs its own fixture and its own
	// run.
	Runs []Run `json:"runs"`
}

// Run is one derivation: one fixture, one operation, one measured action set.
type Run struct {
	Operation Operation `json:"operation"`
	// Fixture is the directory the run applied, relative to the repository root.
	// For a measurement predating the harness it says so instead.
	Fixture string `json:"fixture"`
	// DerivedAt is the date of the run, as YYYY-MM-DD.
	DerivedAt string `json:"derived_at"`
	// ProviderVersion pins what the result is valid for. A derived set is only
	// true for the AWS provider version that produced it, and they drift.
	ProviderVersion string `json:"provider_version"`
	// Sufficiency is "proven" when an apply succeeded with exactly Actions.
	// Anything else cannot support a verification claim.
	Sufficiency string `json:"sufficiency"`
	// Minimality is "proven" when removing each action in turn broke the apply,
	// "partial" when some removals failed for a reason weaker than a clean
	// denial, and "unproven" when minimisation was not run. It does NOT gate a
	// verification claim: over-reporting fails safe, so sufficiency is what
	// matters and minimality is recorded separately.
	Minimality string `json:"minimality"`
	// Attempts is how many applies the run took. Informational.
	Attempts int `json:"attempts,omitempty"`
	// Actions is the measured set: what the scratch role held when the apply
	// succeeded.
	Actions []string `json:"actions"`
	// Backfilled marks a run transcribed from an entry's prose notes when this
	// format was introduced, rather than written by the harness at the time. It
	// is weaker evidence — the transcription is only as good as the note — and
	// saying so is the point.
	Backfilled bool `json:"backfilled,omitempty"`
	// Note records anything about this run a reader needs, such as which
	// attribute a two-phase update fixture varied.
	Note string `json:"note,omitempty"`
}

// LoadEvidence reads every evidence file in fsys, keyed by resource type.
func LoadEvidence(fsys fs.FS) (map[string]Evidence, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading the evidence directory: %w", err)
	}
	out := map[string]Evidence{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		var ev Evidence
		dec := json.NewDecoder(strings.NewReader(string(body)))
		// A misspelled field is a silent loss of provenance, which defeats the
		// point of recording it.
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ev); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}
		if ev.ResourceType == "" {
			return nil, fmt.Errorf("%s: resource_type is required", e.Name())
		}
		want := ev.ResourceType + ".json"
		if e.Name() != want {
			return nil, fmt.Errorf("%s: declares resource_type %q, so the file should be named %q",
				e.Name(), ev.ResourceType, want)
		}
		if prev, dup := out[ev.ResourceType]; dup {
			return nil, fmt.Errorf("%s: duplicate evidence for %q", e.Name(), prev.ResourceType)
		}
		out[ev.ResourceType] = ev
	}
	return out, nil
}

// VerifiedOps returns the operations this entry claims are proven complete.
func (r Resource) VerifiedOps() []Operation {
	if r.Status == StatusVerified {
		// `verified` covers the whole entry, so every mapped operation is
		// claimed. Validation already rejects combining it with
		// verified_operations.
		ops := make([]Operation, 0, len(r.Operations))
		for op := range r.Operations {
			ops = append(ops, op)
		}
		sort.Slice(ops, func(i, j int) bool { return ops[i] < ops[j] })
		return ops
	}
	return r.VerifiedOperations
}

// PossibleActions is every action this operation could require, ignoring `when`
// gates entirely — the operation's own actions, the read set, and any
// cross-resource references scoped to this operation.
//
// Gates are ignored on purpose. This answers "is this action part of the entry at
// all", which is what evidence has to be checked against: a run measured one
// fixture's shape, and a gated action it did not exercise is still legitimately
// in the entry.
func (r Resource) PossibleActions(op Operation) []string {
	var out []string
	for _, a := range r.Operations[op] {
		out = append(out, a.Action)
	}
	out = append(out, r.ReadActions...)
	for _, ref := range r.References {
		if len(ref.Operations) == 0 || containsOp(ref.Operations, op) {
			out = append(out, ref.Action)
		}
	}
	return dedupeSortedStrings(out)
}

func containsOp(ops []Operation, want Operation) bool {
	for _, o := range ops {
		if o == want {
			return true
		}
	}
	return false
}

func dedupeSortedStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// CheckEvidence reports every way the evidence files and the database disagree.
// An empty result means they are consistent.
//
// THE MATCHING RULE IS ASYMMETRIC, and deliberately so: every measured action
// must appear in the entry, but the entry may hold MORE than any single run
// measured. Both halves matter.
//
// Requiring evidence ⊆ entry is what catches the dangerous edit — deleting a
// measured action from a verified entry, which turns a proven claim into a false
// pass. Not requiring equality is what keeps the database honest in the other
// direction: an entry legitimately retains actions no fixture could exercise.
// `iam:DeletePolicyVersion` is the worked example — minimisation proved it
// droppable for a one-version update, and it is still required once a policy
// reaches AWS's five-version cap, which no single-apply fixture can reach.
// Demanding equality would force that action out and ship the false pass.
func (d *Database) CheckEvidence(evidence map[string]Evidence) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// Evidence for a type nobody maps is dead weight that hides a rename.
	for typ := range evidence {
		if _, ok := d.Lookup(typ); !ok {
			add("evidence/%s.json: no such resource type in the database (renamed or removed?)", typ)
		}
	}

	for _, typ := range d.Types() {
		r, _ := d.Lookup(typ)
		ev, hasEvidence := evidence[typ]

		byOp := map[Operation][]Run{}
		for i, run := range ev.Runs {
			where := fmt.Sprintf("evidence/%s.json run %d", typ, i)
			if run.Operation == "" {
				add("%s: operation is required", where)
				continue
			}
			if _, mapped := r.Operations[run.Operation]; !mapped {
				add("%s: records operation %q, which the entry does not map", where, run.Operation)
				continue
			}
			byOp[run.Operation] = append(byOp[run.Operation], run)

			// Provenance. A transcript with no date or provider version cannot
			// be re-checked, and a derived set is only valid for the provider
			// that produced it.
			for field, value := range map[string]string{
				"fixture":          run.Fixture,
				"derived_at":       run.DerivedAt,
				"provider_version": run.ProviderVersion,
				"sufficiency":      run.Sufficiency,
				"minimality":       run.Minimality,
			} {
				if strings.TrimSpace(value) == "" {
					add("%s: %s is required", where, field)
				}
			}
			if len(run.Actions) == 0 {
				add("%s: actions is empty; a run that measured nothing is not evidence", where)
			}

			// Every measured action must be in the entry. This is the check that
			// makes deleting one fail.
			possible := map[string]bool{}
			for _, a := range r.PossibleActions(run.Operation) {
				possible[a] = true
			}
			for _, a := range run.Actions {
				if !possible[a] {
					add("%s: measured %s, which the entry no longer lists for %s — "+
						"either the entry dropped a proven action or the evidence is stale",
						where, a, run.Operation)
				}
			}
		}

		// Every verified operation needs a run behind it, and that run has to
		// have proven sufficiency — the claim is precisely that the mapped set is
		// enough.
		for _, op := range r.VerifiedOps() {
			if !hasEvidence {
				add("%s claims %s is verified but mappings/evidence/%s.json does not exist", typ, op, typ)
				continue
			}
			runs := byOp[op]
			if len(runs) == 0 {
				add("%s claims %s is verified but no evidence run records that operation", typ, op)
				continue
			}
			var proven bool
			for _, run := range runs {
				if run.Sufficiency == SufficiencyProven {
					proven = true
				}
			}
			if !proven {
				add("%s claims %s is verified but no run has sufficiency %q; "+
					"a verification claim is exactly the claim that the mapped set is sufficient",
					typ, op, SufficiencyProven)
			}
		}
	}

	sort.Strings(problems)
	return problems
}

// Sufficiency and minimality values, matching what cmd/derive reports.
const (
	SufficiencyProven = "proven"
	// MinimalityUnproven means minimisation was not run, which is normal for an
	// expensive resource and does not weaken a verification claim: over-reporting
	// fails safe.
	MinimalityUnproven = "unproven"
)
