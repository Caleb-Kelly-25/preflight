package mapping

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// ContextKeySource names the Terraform attribute that supplies a condition-key
// value.
//
// Deliberately not an expression language. A contributor must be able to check
// an entry by eye against AWS's documentation, which is the whole reason the
// mapping database is open.
type ContextKeySource struct {
	// From is the Terraform attribute to read.
	From string `yaml:"from"`
	// Type is the IAM context value type. Defaults to string.
	Type finding.ContextValueType `yaml:"type,omitempty"`
}

// ContextValue sources a value for one condition key from the planned
// attributes.
//
// ok=false means we could not source it, which the caller MUST turn into a
// confidence downgrade rather than a silent omission: an unsupplied key can
// produce a confident "allowed" based on a value AWS substituted, with nothing
// in MissingContextValues to warn us. See docs/DESIGN.md §7.3.
func (r Resource) ContextValue(key string, attrs, unknown map[string]any) ([]string, finding.ContextValueType, bool) {
	src, subKey, ok := r.contextSource(key)
	if !ok {
		return nil, "", false
	}

	if isUnknown(unknown, src.From) {
		return nil, "", false
	}
	raw, present := attrs[src.From]
	if !present || raw == nil {
		return nil, "", false
	}

	typ := src.Type
	if typ == "" {
		typ = finding.ContextString
	}

	// A glob key such as "aws:RequestTag/*" indexes into a map attribute: the
	// tail after the slash is the map subkey.
	if subKey != "" {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, "", false
		}
		v, ok := lookupFold(m, subKey)
		if !ok {
			return nil, "", false
		}
		s, ok := scalar(v)
		if !ok {
			return nil, "", false
		}
		return []string{s}, typ, true
	}

	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := scalar(item)
			if !ok {
				return nil, "", false
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, "", false
		}
		sort.Strings(out)
		return out, typ, true
	default:
		s, ok := scalar(raw)
		if !ok {
			return nil, "", false
		}
		return []string{s}, typ, true
	}
}

// contextSource finds the declaration covering key, returning the map subkey
// when the match came from a "PREFIX/*" glob.
func (r Resource) contextSource(key string) (src ContextKeySource, subKey string, ok bool) {
	if len(r.ContextKeys) == 0 {
		return ContextKeySource{}, "", false
	}

	// Exact declarations win over globs.
	for declared, s := range r.ContextKeys {
		if strings.EqualFold(declared, key) {
			return s, "", true
		}
	}

	for declared, s := range r.ContextKeys {
		prefix, isGlob := strings.CutSuffix(declared, "*")
		if !isGlob {
			continue
		}
		if len(key) > len(prefix) && strings.EqualFold(key[:len(prefix)], prefix) {
			return s, key[len(prefix):], true
		}
	}
	return ContextKeySource{}, "", false
}

// lookupFold reads a map key, preferring an exact match and falling back to a
// case-insensitive one. Terraform tag keys are case-sensitive, but IAM matches
// the condition key case-insensitively, so both are worth trying.
func lookupFold(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// scalar renders a JSON scalar as the string IAM expects. Anything else — a
// nested object, a null — is not sourceable, and saying so is safer than
// guessing at a rendering.
func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case float64:
		// Plan JSON numbers decode as float64. Render integers without a
		// trailing ".0", which IAM numeric conditions would reject.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), true
		}
		return strconv.FormatFloat(t, 'f', -1, 64), true
	default:
		return "", false
	}
}
