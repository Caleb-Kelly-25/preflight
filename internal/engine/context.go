package engine

import (
	"github.com/Caleb-Kelly-25/preflight/internal/finding"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
)

// RequestedRegionKey is the condition key that produced the silent-allow trap.
const RequestedRegionKey = "aws:RequestedRegion"

// autoPopulated are the condition keys the simulator fills in from the
// principal itself. Supplying them is redundant.
//
// aws:RequestedRegion is deliberately NOT in this set even though the simulator
// does populate it — see buildContext.
//
// Source: docs/DESIGN.md §7.2, from the IAM User Guide.
var autoPopulated = map[string]bool{
	"aws:PrincipalAccount":            true,
	"aws:PrincipalId":                 true,
	"aws:PrincipalType":               true,
	"aws:Type":                        true,
	"aws:UserId":                      true,
	"aws:UserName":                    true,
	"aws:PrincipalOrgID":              true,
	"aws:PrincipalOrgMasterAccountId": true,
	"aws:PrincipalOrgPaths":           true,
}

// buildContext decides what condition-key values to send, and records which
// referenced keys we could not source.
//
// The region is handled first and unconditionally. Measured 2026-08-31 (R1):
// the simulator auto-populates aws:RequestedRegion with a FIXED us-east-1,
// regardless of which regional endpoint is called (R3 rules out endpoint-based
// population). So a policy gated on us-east-1, with no value supplied, returns
// ALLOWED with an EMPTY MissingContextValues. If the real deploy targets any
// other region the apply fails, and we would have reported a confident pass.
//
// Letting this key default is the single most dangerous thing this package
// could do, so it is always supplied — and when no region is determinable it is
// recorded as unsupplied rather than silently omitted, because an omission
// re-arms the trap.
func (opts Options) buildContext(res mapping.Resource, attrs, unknown map[string]any) (supplied []finding.ContextEntry, unsupplied []string) {
	have := map[string]bool{}

	add := func(e finding.ContextEntry) {
		supplied = append(supplied, e)
		have[lower(e.Key)] = true
	}
	miss := func(key string) {
		if have[lower(key)] {
			return
		}
		for _, u := range unsupplied {
			if finding.EqualKey(u, key) {
				return
			}
		}
		unsupplied = append(unsupplied, key)
	}

	// 1. Region, always. Per-resource first: the AWS provider v6 puts a region
	//    argument on every resource, and provider aliases have always allowed
	//    one plan to span regions, so a single run-level value can be wrong.
	if region, ok := resourceRegion(attrs, unknown); ok {
		add(finding.ContextEntry{Key: RequestedRegionKey, Type: finding.ContextString, Values: []string{region}})
	} else if opts.Region != "" {
		add(finding.ContextEntry{Key: RequestedRegionKey, Type: finding.ContextString, Values: []string{opts.Region}})
	} else {
		miss(RequestedRegionKey)
	}

	// 2. Explicit --context overrides win over anything we could source.
	for _, e := range opts.ExtraContext {
		if have[lower(e.Key)] {
			// Replace rather than append; two entries for one key is invalid.
			for i := range supplied {
				if finding.EqualKey(supplied[i].Key, e.Key) {
					supplied[i] = e
					break
				}
			}
			continue
		}
		add(e)
	}

	// 3. Everything the principal's policies actually reference. Keys nobody
	//    references are not worth supplying: sending them would fragment
	//    batching for no gain.
	for _, key := range opts.PolicyContextKeys {
		if autoPopulated[key] || have[lower(key)] {
			continue
		}
		values, typ, ok := res.ContextValue(key, attrs, unknown)
		if !ok {
			// Not sourceable, or the source attribute is unknown until apply.
			// Never invent a value — a wrong value produces a confident denial
			// just as readily as a missing one produces a confident allow.
			miss(key)
			continue
		}
		add(finding.ContextEntry{Key: key, Type: typ, Values: values})
	}

	finding.SortContext(supplied)
	return supplied, unsupplied
}

// resourceRegion reads the per-resource region argument when the plan sets it
// to a known literal.
func resourceRegion(attrs, unknown map[string]any) (string, bool) {
	if attrs == nil {
		return "", false
	}
	if u, ok := unknown["region"].(bool); ok && u {
		return "", false
	}
	s, ok := attrs["region"].(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
