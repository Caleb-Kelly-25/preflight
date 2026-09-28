// Package report renders a finding.Report in the output formats the spec calls
// for: a human-readable CLI report, machine-readable JSON, and SARIF for GitHub
// code scanning.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// Format is an output format.
type Format string

const (
	FormatText  Format = "text"
	FormatJSON  Format = "json"
	FormatSARIF Format = "sarif"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatText, FormatJSON, FormatSARIF:
		return Format(s), nil
	default:
		return "", fmt.Errorf("unknown format %q (want text, json, or sarif)", s)
	}
}

// SchemaVersion identifies the JSON output contract.
//
// Teams build dashboards on this, and they are exactly the users who later want
// the platform layer — breaking their integration would be expensive twice over.
// Bump the major only for a breaking change.
const SchemaVersion = "1.0"

// WriteOptions tunes rendering.
type WriteOptions struct {
	// Explain adds the supplied context, the simulated ARN, and each action's
	// raw decision. It is the only mitigation available for the SCP diagnostic
	// blind spot, where AWS deliberately withholds why an SCP denied.
	Explain bool

	// Locations resolves a resource address to a source position, for SARIF.
	// The other formats ignore it.
	//
	// nil is a supported state, not an error: WriteSARIF then drops every
	// result and says so, loudly, rather than emitting location-less results
	// GitHub would accept and then fail to place.
	Locations SourceIndex

	// ToolVersion is reported as the SARIF driver's semanticVersion. GitHub
	// groups analyses by it, so a released binary should set it.
	ToolVersion string

	// Warn receives degradation notices that belong on the terminal rather than
	// in the report — SARIF results dropped for want of a location, above all.
	// A machine-readable report goes to stdout and must stay parseable, so the
	// human-facing half of the message needs a separate channel. May be nil.
	Warn func(string)
}

// Write renders the report in the requested format.
func Write(w io.Writer, r *finding.Report, f Format, opts WriteOptions) error {
	switch f {
	case FormatJSON:
		return WriteJSON(w, r)
	case FormatSARIF:
		return WriteSARIF(w, r, opts)
	default:
		return WriteText(w, r, opts)
	}
}

// WriteJSON emits the machine-readable form, for teams building their own
// dashboards or piping into other tooling.
func WriteJSON(w io.Writer, r *finding.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		SchemaVersion string `json:"schema_version"`
		*finding.Report
		Summary finding.Counts `json:"summary"`
	}{SchemaVersion: SchemaVersion, Report: r, Summary: r.Counts()})
}

// WriteText renders the human-readable report, grouped by confidence level so
// that what was actually verified is never visually conflated with what was not.
func WriteText(w io.Writer, r *finding.Report, opts WriteOptions) error {
	var b strings.Builder

	fmt.Fprintf(&b, "preflight — IAM permission check\n")
	if r.PrincipalARN != "" {
		fmt.Fprintf(&b, "principal: %s\n", r.PrincipalARN)
	}
	b.WriteString("\n")

	for _, warn := range r.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", warn)
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n")
	}

	if len(r.Findings) == 0 {
		b.WriteString("No AWS resource changes in this plan. Nothing to check.\n")
		_, err := io.WriteString(w, b.String())
		return err
	}

	// Denied findings first and on their own: they are the actionable ones.
	var denied []finding.Finding
	byLevel := map[finding.Level][]finding.Finding{}
	for _, f := range r.Findings {
		if f.Denied() {
			denied = append(denied, f)
			continue
		}
		byLevel[f.Level] = append(byLevel[f.Level], f)
	}

	if len(denied) > 0 {
		fmt.Fprintf(&b, "MISSING PERMISSIONS (%d)\n", len(denied))
		b.WriteString("This plan will fail at apply time.\n\n")
		writeGroup(&b, denied, true, opts)
	}

	for _, lvl := range []finding.Level{finding.LevelVerified, finding.LevelLikely, finding.LevelUnchecked} {
		group := byLevel[lvl]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s (%d)\n", strings.ToUpper(string(lvl)), len(group))
		fmt.Fprintf(&b, "%s\n\n", levelBlurb(lvl))
		writeGroup(&b, group, false, opts)
	}

	writeSuppressedNote(&b, r)
	writeStats(&b, r, opts)

	c := r.Counts()
	fmt.Fprintf(&b, "summary: %d verified, %d likely, %d unchecked, %d with missing permissions\n",
		c.Verified, c.Likely, c.Unchecked, c.Denied)

	_, err := io.WriteString(w, b.String())
	return err
}

// writeSuppressedNote explains denials we deliberately did not report.
//
// Without this a user who knows AWS said "no" would see preflight say nothing
// and reasonably conclude the tool is broken. Saying why is what makes the
// suppression trustworthy rather than suspicious.
func writeSuppressedNote(b *strings.Builder, r *finding.Report) {
	var n int
	for _, f := range r.Findings {
		n += len(f.SuppressedDenials())
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(b, "NOTE: %d denial(s) were not reported as missing permissions.\n", n)
	b.WriteString("  AWS returned a denial, but the question we asked could not distinguish a real\n")
	b.WriteString("  gap from an artefact of how we had to ask it — an unresolvable ARN, or a\n")
	b.WriteString("  condition key we could not supply. Those findings are Unchecked, not passing.\n")
	b.WriteString("  Run with --explain to see each one.\n\n")
}

func writeGroup(b *strings.Builder, fs []finding.Finding, showActions bool, opts WriteOptions) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].ResourceAddress != fs[j].ResourceAddress {
			return fs[i].ResourceAddress < fs[j].ResourceAddress
		}
		return fs[i].Operation < fs[j].Operation
	})

	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	for _, f := range fs {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", f.ResourceAddress, f.Operation, reasonSummary(f))
		if showActions {
			for _, a := range f.MissingActions() {
				fmt.Fprintf(tw, "  \t\t  missing: %s\n", a)
			}
		}
		if opts.Explain {
			writeExplain(tw, f)
		}
	}
	_ = tw.Flush()
	b.WriteString("\n")
}

// writeExplain shows exactly what we asked and what came back.
func writeExplain(tw *tabwriter.Writer, f finding.Finding) {
	if f.SimulatedARN != "" {
		fmt.Fprintf(tw, "  \t\t  scoped to: %s\n", f.SimulatedARN)
	}
	for _, e := range f.SuppliedContext {
		fmt.Fprintf(tw, "  \t\t  context: %s = %s\n", e.Key, strings.Join(e.Values, ","))
	}
	for _, k := range f.UnsuppliedContextKeys {
		fmt.Fprintf(tw, "  \t\t  context: %s = (could not supply)\n", k)
	}
	for _, a := range f.Actions {
		line := fmt.Sprintf("  \t\t  %s -> %s", a.Action, a.Decision)
		// A cross-resource action such as iam:PassRole is authorised against
		// the resource being handed over, not against the one being changed.
		// Showing the finding's own ARN for it would state the wrong scope.
		if a.ResourceARN != "" && a.ResourceARN != f.SimulatedARN {
			line += fmt.Sprintf(" (on %s)", a.ResourceARN)
		}
		if a.Inconclusive {
			line += fmt.Sprintf(" (not acted on: %s)", reasonText(a.InconclusiveReason))
		}
		if len(a.MissingContextValues) > 0 {
			line += fmt.Sprintf(" [missing: %s]", strings.Join(a.MissingContextValues, ", "))
		}
		fmt.Fprintln(tw, line)
	}
}

func reasonSummary(f finding.Finding) string {
	if len(f.Reasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(f.Reasons))
	for _, r := range f.Reasons {
		text := reasonText(r)
		// Name the keys rather than leaving a vague hedge — a reason the reader
		// cannot act on is a reason they learn to ignore.
		if r == finding.ReasonConditionKeysUnknown && len(f.UnsuppliedContextKeys) > 0 {
			text += ": " + strings.Join(f.UnsuppliedContextKeys, ", ")
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ")
}

func reasonText(r finding.Reason) string {
	switch r {
	case finding.ReasonResourcePolicyNotEvaluated:
		return "resource policy not evaluated"
	case finding.ReasonRCPNotEvaluated:
		return "RCPs not evaluated"
	case finding.ReasonARNUnresolved:
		// The scope actually used is printed per finding under --explain: it is
		// "*" when nothing was derivable, or a representative name when the
		// entry supplied a name_prefix. Naming "*" here would now be wrong for
		// the second case.
		return "exact ARN unknown until apply, so denials are inconclusive"
	case finding.ReasonConditionKeysUnknown:
		return "policy conditions not evaluated"
	case finding.ReasonMappingUnverified:
		return "mapping entry not yet verified"
	case finding.ReasonNoMapping:
		return "resource type not in mapping database"
	case finding.ReasonOperationNotMapped:
		return "this operation not in mapping database"
	case finding.ReasonSimulationFailed:
		return "not simulated"
	default:
		return string(r)
	}
}

func levelBlurb(l finding.Level) string {
	switch l {
	case finding.LevelVerified:
		return "  Simulated against the identity policy with nothing left unevaluated."
	case finding.LevelLikely:
		return "  Identity policy allows these, but something that could still deny was not checked."
	default:
		return "  Not checked. Do not read these as safe."
	}
}

// writeStats prints what the simulator actually did, under --explain only.
//
// Behind --explain because it answers a maintainer's question, not a user's: a
// team wants to know whether their plan can apply, and a line about cache hits
// competes with that for attention. But the numbers have to be REACHABLE, because
// IAM's simulate throttling limits are unpublished and the only way to learn them
// is from real runs. They were computed and discarded until 2026-09-28.
//
// Nothing is printed when no simulation ran — every finding decided without asking
// AWS, or the call failed. Printing zeros there would read as "we asked and got
// nothing", which is a different and more alarming claim.
func writeStats(b *strings.Builder, r *finding.Report, opts WriteOptions) {
	if !opts.Explain || r.Stats == nil {
		return
	}
	st := r.Stats

	b.WriteString("simulation\n")
	fmt.Fprintf(b, "  %d API call(s), %d evaluation(s) returned", st.Calls, st.Evaluations)
	// The ratio is why both are reported: it measures batching. Per-resource
	// condition-key values fragment batches, and when they do, calls climb toward
	// evaluations.
	if st.Calls > 0 && st.Evaluations > 0 {
		fmt.Fprintf(b, " (%.1f per call)", float64(st.Evaluations)/float64(st.Calls))
	}
	b.WriteString("\n")

	if st.Pages > 1 {
		fmt.Fprintf(b, "  %d pages\n", st.Pages)
	}
	if st.CacheHits > 0 {
		fmt.Fprintf(b, "  %d cache hit(s)\n", st.CacheHits)
	}
	// Throttling is the datum the unpublished-limits question needs, and the
	// signal that a larger plan will start failing.
	if st.Throttles > 0 || st.Retries > 0 {
		fmt.Fprintf(b, "  %d throttle(s), %d retry(ies)\n", st.Throttles, st.Retries)
	}
	fmt.Fprintf(b, "  %dms elapsed\n\n", st.ElapsedMS)
}
