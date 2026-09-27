package awsref

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
)

// Severity separates findings that need a fix from findings that need a look.
type Severity int

const (
	// Info: worth knowing, not wrong. An action that authorizes only against
	// "*" scoped to a specific ARN still simulates correctly, because the "*"
	// a policy must use to grant it matches any ARN we ask about.
	Info Severity = iota
	// Warn: the entry may be wrong, but the failure direction is a visible
	// false positive rather than a silent pass.
	Warn
	// Error: the entry is wrong in a way that can produce a false "allowed".
	Error
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "ERROR"
	case Warn:
		return "WARN"
	default:
		return "INFO"
	}
}

// Finding is one thing the check noticed.
type Finding struct {
	Severity     Severity
	ResourceType string // the Terraform type, e.g. aws_s3_bucket
	Action       string
	Detail       string
}

func (f Finding) String() string {
	return fmt.Sprintf("%-5s %-46s %-38s %s", f.Severity, f.ResourceType, f.Action, f.Detail)
}

// Fetcher returns the reference document for a service. Injected so the check
// is testable without network access.
type Fetcher func(service string) (*Service, error)

// Check verifies every entry's actions against the resource type its arn_format
// names.
//
// Unreachable services are reported as Info rather than failing: this reads a
// live AWS endpoint, and a check that fails the build because a third party had
// an outage is a check people learn to ignore. Only a genuine mismatch is an
// error.
func Check(db *mapping.Database, fetch Fetcher) []Finding {
	var out []Finding
	cache := map[string]*Service{}

	get := func(service string) *Service {
		if s, ok := cache[service]; ok {
			return s
		}
		s, err := fetch(service)
		if err != nil {
			out = append(out, Finding{
				Severity: Info,
				Action:   service,
				Detail:   fmt.Sprintf("could not fetch the service reference, so nothing in it was checked: %v", err),
			})
			s = nil
		}
		cache[service] = s
		return s
	}

	for _, typ := range db.Types() {
		res, ok := db.Lookup(typ)
		if !ok {
			continue
		}
		out = append(out, checkEntry(res, typ, get)...)
	}
	return out
}

func checkEntry(res mapping.Resource, typ string, get func(string) *Service) []Finding {
	var out []Finding

	svc := get(res.Service)
	if svc == nil {
		return out
	}

	// TWO INDEPENDENT CHECKS LIVE IN THIS FUNCTION, and conflating them cost real
	// coverage until 2026-09-26:
	//
	//   1. do the action NAMES exist?
	//   2. does each action authorize against the resource type arn_format names?
	//
	// Only the second needs an arn_format. Returning early when there is nothing to
	// scope-check silently skipped the FIRST check too, so every entry with a
	// passthrough arn_format — aws_lb, aws_lb_listener, aws_lb_target_group,
	// aws_kms_alias and the SNS pair — had no action-name validation at all. A typo
	// in any of them would have passed CI.
	if res.ARNFormat == "" || isPassthrough(res.ARNFormat) {
		if isPassthrough(res.ARNFormat) {
			// A format that is nothing but a single variable is a passthrough: the
			// attribute already holds a complete ARN, so there is no shape to compare
			// against anything. Reporting that as a mismatch would be this tool's own
			// false positive, which costs exactly what a false positive in the product
			// costs — it teaches the reader to skim past the output.
			out = append(out, Finding{
				Severity: Info, ResourceType: typ, Action: "(arn_format)",
				Detail: fmt.Sprintf("%s is a passthrough of an attribute that already holds an ARN; "+
					"its shape cannot be checked here, only by a real apply", res.ARNFormat),
			})
		}
		return append(out, checkActionNames(svc, res, typ)...)
	}

	matched := svc.ResourceTypesMatching(res.ARNFormat)
	if len(matched) == 0 {
		out = append(out, Finding{
			Severity:     Error,
			ResourceType: typ,
			Action:       "(arn_format)",
			Detail: fmt.Sprintf("no resource type in %q has this ARN shape: %s — "+
				"the simulation is scoped to an ARN AWS never authorizes against",
				res.Service, res.ARNFormat),
		})
		return out
	}
	want := map[string]bool{}
	for _, m := range matched {
		want[m] = true
	}

	for _, action := range entryActions(res) {
		if !sameService(action, res.Service) {
			out = append(out, crossService(typ, action))
			continue
		}
		types, scopeless, known := svc.ResourceTypesFor(action)
		switch {
		case !known:
			out = append(out, Finding{
				Severity: Warn, ResourceType: typ, Action: action,
				Detail: "not listed in the service reference — check the spelling",
			})
		case scopeless:
			out = append(out, Finding{
				Severity: Info, ResourceType: typ, Action: action,
				Detail: "authorizes only against \"*\"; scoping it is harmless but has no effect",
			})
		default:
			if !anyIn(types, want) {
				sort.Strings(types)
				out = append(out, mismatch(svc, res, typ, action, types, matched))
			}
		}
	}
	return out
}

// mismatch grades a scoping mismatch by which way it fails, because the two
// directions are not remotely equivalent and grading them the same makes the
// output unreadable.
//
// If the entry's ARN is NARROWER than the type the action authorizes against —
// a log stream inside a log group — then a policy written against the real type
// will not match our query, the simulation is denied, and the result is a
// visible false positive. Expensive, but safe.
//
// If the entry's ARN is BROADER, a policy naming the broad form allows our
// query while the real, narrower call is denied. That is a false "allowed" for
// a deploy that cannot succeed, which is the failure this project exists to
// prevent.
func mismatch(svc *Service, res mapping.Resource, typ, action string, actionTypes, entryTypes []string) Finding {
	entryShape := normalizeARN(res.ARNFormat)
	for _, t := range actionTypes {
		for _, r := range svc.Resources {
			if r.Name != t {
				continue
			}
			for _, f := range r.ARNFormats {
				shape := normalizeARN(f)
				if shape == entryShape {
					continue
				}
				if strings.HasPrefix(entryShape, shape) {
					return Finding{
						Severity: Warn, ResourceType: typ, Action: action,
						Detail: fmt.Sprintf("authorizes against %v; the entry scopes it to the narrower %v, "+
							"so a policy naming the real type is reported as denied — over-reports, fails safe",
							actionTypes, entryTypes),
					}
				}
			}
		}
	}
	return Finding{
		Severity: Error, ResourceType: typ, Action: action,
		Detail: fmt.Sprintf("authorizes against %v, but the entry scopes it to the broader %v — "+
			"a policy naming the broad form would be reported as ALLOWED while the real call is denied",
			actionTypes, entryTypes),
	}
}

// entryActions is every action the entry scopes to its own ARN. References are
// deliberately excluded: they are evaluated against a DIFFERENT resource's ARN,
// so checking them against this entry's arn_format would report a mismatch that
// is the whole point of the feature.
func entryActions(res mapping.Resource) []string {
	seen := map[string]bool{}
	var out []string
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, op := range []mapping.Operation{mapping.OpCreate, mapping.OpUpdate, mapping.OpDelete} {
		for _, a := range res.Operations[op] {
			add(a.Action)
		}
	}
	for _, a := range res.ReadActions {
		add(a)
	}
	sort.Strings(out)
	return out
}

func anyIn(types []string, want map[string]bool) bool {
	for _, t := range types {
		if want[t] {
			return true
		}
	}
	return false
}

// Worst returns the highest severity present, so a caller can decide an exit
// code without re-scanning.
func Worst(findings []Finding) Severity {
	worst := Info
	for _, f := range findings {
		if f.Severity > worst {
			worst = f.Severity
		}
	}
	return worst
}

// checkActionNames verifies only that the action names exist, for entries whose
// arn_format cannot be scope-checked. See the comment in checkEntry for why this is
// separate: skipping it along with the scope check left a whole class of entry
// unvalidated.
func checkActionNames(svc *Service, res mapping.Resource, typ string) []Finding {
	var out []Finding
	for _, action := range entryActions(res) {
		if !sameService(action, res.Service) {
			out = append(out, crossService(typ, action))
			continue
		}
		if _, _, known := svc.ResourceTypesFor(action); !known {
			out = append(out, Finding{
				Severity: Warn, ResourceType: typ, Action: action,
				Detail: "not listed in the service reference — check the spelling",
			})
		}
	}
	return out
}

// sameService reports whether an action belongs to the entry's own service.
func sameService(action, service string) bool {
	prefix, _, found := strings.Cut(action, ":")
	return found && strings.EqualFold(prefix, service)
}

// crossService reports an action from another service without judging its spelling.
//
// This checker fetches ONE service reference per entry — the entry's own — so an
// action like iam:CreateServiceLinkedRole simply is not in the document being
// consulted. Reporting that as "check the spelling" is this tool's own false
// positive, and it fired on aws_db_subnet_group the moment a real cross-service
// requirement was added.
//
// Those actions are not unchecked: TestShippedDatabase's crossServiceActions
// allowlist pins each one by exact name, so a typo fails the unit tests instead.
func crossService(typ, action string) Finding {
	return Finding{
		Severity: Info, ResourceType: typ, Action: action,
		Detail: "belongs to another service, so this entry's reference cannot confirm it; " +
			"its spelling is pinned by crossServiceActions in TestShippedDatabase",
	}
}
