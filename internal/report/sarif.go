package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// SourceIndex resolves a Terraform resource address to the source position of
// the block that declared it. internal/hclsrc implements it by parsing the
// configuration; the plan JSON has no positions to offer.
//
// It is stated in primitives rather than a shared struct so that this package
// does not import internal/hclsrc, and therefore does not pull hashicorp/hcl
// into the import graph of everything that renders a report.
type SourceIndex interface {
	Lookup(address string) (file string, line int, ok bool)
}

const (
	sarifVersion   = "2.1.0"
	sarifSchemaURI = "https://json.schemastore.org/sarif-2.1.0.json"
	toolName       = "preflight"
	toolInfoURI    = "https://github.com/Caleb-Kelly-25/preflight"
)

// SARIF rule identifiers. GitHub shows these in the code-scanning UI and uses
// them to group alerts across runs, so they are a contract in the same way the
// exit codes are: changing one splits a team's alert history in two.
const (
	ruleMissingPermission = "preflight/missing-permission"
	ruleUnchecked         = "preflight/unchecked"
	ruleNotVerified       = "preflight/not-verified"
)

// Mapping the three confidence states onto SARIF's four levels is the one
// judgement call in this file, so it is written down rather than inferred:
//
//	denied    -> error    The apply will fail. It is the one state we are
//	                      certain about and the only one worth blocking on.
//	unchecked -> warning  Nothing was meaningfully checked. "Not checked" must
//	                      never render as clean, but it is not evidence of a
//	                      gap either, so it is not an error.
//	likely    -> note     The identity policy allowed it and something that
//	                      could still deny was not evaluated. Informational:
//	                      raising it to warning would put a marker on most
//	                      lines of an ordinary configuration, which is exactly
//	                      how teams learn to ignore a tool.
//	verified  -> omitted  A verified pass is not a problem, and SARIF results
//	                      are problems. Emitting them would bury the ones that
//	                      matter under a wall of green.
const (
	levelError   = "error"
	levelWarning = "warning"
	levelNote    = "note"
)

// WriteSARIF emits SARIF 2.1.0 for GitHub code scanning.
//
// Every result needs a file and a line or GitHub silently fails to place the
// annotation, and the plan JSON has neither — opts.Locations supplies them from
// a pass over the .tf files. When a finding's address cannot be located the
// result is DROPPED rather than emitted without a location, because a
// location-less result makes the feature look broken instead of absent.
//
// Dropping is never silent, because a SARIF run with no results reads to GitHub
// as a clean bill of health, and a clean bill of health we did not earn is the
// one failure mode this tool exists to avoid.
//
// How loudly we can say so depends on the channel, and one assumption here was
// wrong. Checked against GitHub's supported-properties list on 2026-09-25:
// **neither `invocation.executionSuccessful` nor `toolExecutionNotifications`
// is a supported property**, and unsupported properties are ignored. Both are
// still emitted — they are correct SARIF and other consumers read them — but
// neither reaches a GitHub user, so neither can be relied on.
//
// That leaves two channels that do work. opts.Warn goes to the terminal, which
// is read when someone is looking. And if EVERY result was dropped there is no
// report at all, so this returns an error: the caller exits 2, "the tool could
// not run", which is exactly what happened. An empty SARIF uploaded from a
// green step is the false pass in its most deniable form, and the exit code is
// the only signal CI cannot overlook.
//
// A partial drop still produces a useful report, so it warns rather than fails.
func WriteSARIF(w io.Writer, r *finding.Report, opts WriteOptions) error {
	findings := append([]finding.Finding(nil), r.Findings...)
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].ResourceAddress != findings[j].ResourceAddress {
			return findings[i].ResourceAddress < findings[j].ResourceAddress
		}
		return findings[i].Operation < findings[j].Operation
	})

	results := make([]sarifResult, 0, len(findings))
	var dropped []string
	for _, f := range findings {
		ruleID, level, emit := classifySARIF(f)
		if !emit {
			continue
		}

		// The address goes over unaltered: resolving a count or for_each
		// instance back to its block is the index's job, and doing it in two
		// places is how the two drift apart.
		var file string
		var line int
		var located bool
		if opts.Locations != nil {
			file, line, located = opts.Locations.Lookup(f.ResourceAddress)
		}
		if !located {
			dropped = append(dropped, f.ResourceAddress+" ("+f.Operation+")")
			continue
		}

		results = append(results, sarifResult{
			RuleID:    ruleID,
			RuleIndex: ruleIndex(ruleID),
			Level:     level,
			Message:   sarifMessage{Text: sarifText(f)},
			Locations: []sarifLocation{{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{URI: file},
					Region:           sarifRegion{StartLine: line},
				},
			}},
			// Keyed on what the finding is about rather than where it sits, so
			// that moving a resource block does not resurface an alert the team
			// already triaged.
			PartialFingerprints: map[string]string{
				"preflightResourceOperation/v1": f.ResourceAddress + "#" + f.Operation,
			},
		})
	}

	notifications := make([]sarifNotification, 0, len(r.Warnings)+1)
	for _, warn := range r.Warnings {
		notifications = append(notifications, sarifNotification{
			Level:   levelWarning,
			Message: sarifMessage{Text: warn},
		})
	}
	if len(dropped) > 0 {
		text := droppedText(dropped, opts.Locations == nil)

		// Nothing located at all. Emitting an empty SARIF here would upload a
		// green analysis for a run that checked nothing GitHub can see, and the
		// in-band notices above cannot correct that because GitHub ignores them.
		if len(results) == 0 {
			if opts.Warn != nil {
				opts.Warn(text)
			}
			return fmt.Errorf("sarif: no finding could be given a source location, so the report would be empty and read as clean: %s", text)
		}

		notifications = append(notifications, sarifNotification{
			Level:   levelError,
			Message: sarifMessage{Text: text},
		})
		if opts.Warn != nil {
			opts.Warn(text)
		}
	}

	log := sarifLog{
		Schema:  sarifSchemaURI,
		Version: sarifVersion,
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:           toolName,
				InformationURI: toolInfoURI,
				SemanticVersion: strings.TrimSpace(strings.TrimPrefix(
					strings.TrimSpace(opts.ToolVersion), "v")),
				Rules: sarifRules(),
			}},
			Results: results,
			Invocations: []sarifInvocation{{
				// False when anything was dropped: the analysis genuinely did
				// not complete, and a green check over an incomplete analysis
				// is the failure this tool exists to prevent.
				ExecutionSuccessful:        len(dropped) == 0,
				ToolExecutionNotifications: notifications,
			}},
		}},
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(log); err != nil {
		return fmt.Errorf("writing sarif report: %w", err)
	}
	return nil
}

// classifySARIF picks the rule and level for a finding, or reports that it
// should not appear in SARIF at all.
func classifySARIF(f finding.Finding) (ruleID, level string, emit bool) {
	switch {
	case f.Denied():
		return ruleMissingPermission, levelError, true
	case f.Level == finding.LevelUnchecked:
		return ruleUnchecked, levelWarning, true
	case f.Level == finding.LevelLikely:
		return ruleNotVerified, levelNote, true
	default:
		return "", "", false
	}
}

// sarifText renders the annotation body. It has to stand alone: a reviewer sees
// it inline on the diff with none of the report's surrounding structure, so the
// address, the operation, what is missing and why all have to be in the one
// sentence.
func sarifText(f finding.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s): ", f.ResourceAddress, f.Operation)

	switch {
	case f.Denied():
		b.WriteString("terraform apply will fail. The deploy identity is missing ")
		b.WriteString(strings.Join(f.MissingActions(), ", "))
		b.WriteString(".")
	case f.Level == finding.LevelUnchecked:
		b.WriteString("not checked, so this must not be read as safe.")
	default:
		b.WriteString("the identity policy allows the required actions, but this was not fully verified.")
	}

	if s := reasonSummary(f); s != "" {
		fmt.Fprintf(&b, " Reason: %s.", s)
	}

	// A denial we could not act on is stated rather than hidden, for the same
	// reason the text report states it: a user who knows AWS said no and sees
	// preflight say nothing concludes the tool is broken.
	if n := len(f.SuppressedDenials()); n > 0 {
		fmt.Fprintf(&b, " AWS denied %d action(s), but the question could not be asked precisely"+
			" enough to act on that; run preflight with --explain to see them.", n)
	}
	return b.String()
}

// maxNamedDrops caps how many addresses the drop notice lists. A plan with a
// misconfigured --config-dir drops everything, and a notification listing four
// hundred addresses is one nobody reads — the count is the part that must land.
const maxNamedDrops = 20

// droppedText explains a gap in the annotations in terms the reader can act on.
func droppedText(dropped []string, noIndex bool) string {
	named := dropped
	suffix := ""
	if len(named) > maxNamedDrops {
		named = named[:maxNamedDrops]
		suffix = fmt.Sprintf(" and %d more", len(dropped)-maxNamedDrops)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d finding(s) were omitted from this SARIF report because their source"+
		" location could not be determined, so they are NOT represented here and this run"+
		" must not be read as clean: %s%s.", len(dropped), strings.Join(named, ", "), suffix)
	if noIndex {
		b.WriteString(" No Terraform configuration was indexed; point --config-dir at the" +
			" directory holding the .tf files.")
	} else {
		b.WriteString(" These addresses were not found in the indexed configuration — check that" +
			" --config-dir matches the configuration the plan was produced from, and note that" +
			" resources inside registry or git modules cannot be located.")
	}
	return b.String()
}

// sarifRules describes the three rules. GitHub renders help.markdown in the
// alert view, which is the only place there is room to explain the confidence
// model to someone who has never run the tool.
func sarifRules() []sarifRule {
	return []sarifRule{
		{
			ID:               ruleMissingPermission,
			Name:             "MissingPermission",
			ShortDescription: sarifMessage{Text: "The deploy identity lacks an IAM permission this change requires"},
			FullDescription: sarifMessage{Text: "iam:SimulatePrincipalPolicy returned a conclusive denial for an " +
				"action this resource change requires. terraform apply will fail."},
			Help: sarifMessage{
				Text: "Grant the named actions to the identity running terraform apply.",
				Markdown: "Grant the named actions to the identity running `terraform apply`.\n\n" +
					"This denial is conclusive: the action was simulated against a resolvable ARN, " +
					"and denials that could be an artefact of how the question had to be asked are " +
					"reported as `preflight/unchecked` instead.",
			},
			DefaultConfiguration: sarifRuleConfig{Level: levelError},
		},
		{
			ID:               ruleUnchecked,
			Name:             "UncheckedResource",
			ShortDescription: sarifMessage{Text: "This change was not checked and must not be read as safe"},
			FullDescription: sarifMessage{Text: "No meaningful permission check was performed — most often because " +
				"the resource type is not in the mapping database, or the resource ARN is not knowable until apply."},
			Help: sarifMessage{
				Text: "Unchecked is not a pass. Treat this resource as unverified.",
				Markdown: "**Unchecked is not a pass.** preflight could not evaluate this change, so it " +
					"carries no information about whether the apply will succeed.\n\n" +
					"The usual fix is a mapping entry for the resource type; contributions are the " +
					"highest-value work on the project.",
			},
			DefaultConfiguration: sarifRuleConfig{Level: levelWarning},
		},
		{
			ID:               ruleNotVerified,
			Name:             "NotFullyVerified",
			ShortDescription: sarifMessage{Text: "Allowed by the identity policy, but something that could still deny was not evaluated"},
			FullDescription: sarifMessage{Text: "The simulation passed against the identity policy and any in-scope SCPs, " +
				"but a resource-based policy, an RCP, an unsupplied condition key, or an unverified mapping entry " +
				"could still deny this at apply time."},
			Help: sarifMessage{
				Text: "Informational. The check passed as far as it could be taken.",
				Markdown: "Informational. The identity policy allows the required actions, but preflight " +
					"could not rule out everything that denies at apply time. The message names which " +
					"of those applies here.",
			},
			DefaultConfiguration: sarifRuleConfig{Level: levelNote},
		},
	}
}

func ruleIndex(id string) int {
	for i, r := range sarifRules() {
		if r.ID == id {
			return i
		}
	}
	return 0
}

// The SARIF 2.1.0 subset preflight emits. Only the properties GitHub reads are
// modelled; the schema is enormous and the rest of it would be noise.

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool        sarifTool         `json:"tool"`
	Results     []sarifResult     `json:"results"`
	Invocations []sarifInvocation `json:"invocations,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name            string      `json:"name"`
	InformationURI  string      `json:"informationUri,omitempty"`
	SemanticVersion string      `json:"semanticVersion,omitempty"`
	Rules           []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	ShortDescription     sarifMessage    `json:"shortDescription"`
	FullDescription      sarifMessage    `json:"fullDescription"`
	Help                 sarifMessage    `json:"help"`
	DefaultConfiguration sarifRuleConfig `json:"defaultConfiguration"`
}

type sarifRuleConfig struct {
	Level string `json:"level"`
}

type sarifMessage struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown,omitempty"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifMessage      `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifInvocation struct {
	ExecutionSuccessful        bool                `json:"executionSuccessful"`
	ToolExecutionNotifications []sarifNotification `json:"toolExecutionNotifications,omitempty"`
}

type sarifNotification struct {
	Level   string       `json:"level"`
	Message sarifMessage `json:"message"`
}
