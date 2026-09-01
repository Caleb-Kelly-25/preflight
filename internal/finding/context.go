package finding

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ContextValueType mirrors the IAM ContextKeyTypeEnum. The simulator rejects a
// context entry whose type does not match how the policy uses the key, so this
// is not cosmetic.
type ContextValueType string

const (
	ContextString     ContextValueType = "string"
	ContextStringList ContextValueType = "stringList"
	ContextNumeric    ContextValueType = "numeric"
	ContextBoolean    ContextValueType = "boolean"
	ContextDate       ContextValueType = "date"
	ContextIP         ContextValueType = "ip"
	ContextARN        ContextValueType = "arn"
)

// ValidContextValueType reports whether t is one the simulator accepts.
func ValidContextValueType(t ContextValueType) bool {
	switch t {
	case ContextString, ContextStringList, ContextNumeric,
		ContextBoolean, ContextDate, ContextIP, ContextARN:
		return true
	default:
		return false
	}
}

// ContextEntry is one condition-key value supplied to the simulator.
//
// This type lives in finding rather than engine because it is part of the JSON
// output contract (see Finding.SuppliedContext) and because both the engine and
// the simulator implementation need it. The engine never imports the simulator,
// so there is no cycle.
type ContextEntry struct {
	Key    string           `json:"key"`
	Type   ContextValueType `json:"type"`
	Values []string         `json:"values"`
}

// SortContext orders entries by key, then by value, so that a given set of
// context always produces the same Fingerprint across runs. Batching groups on
// that fingerprint, so instability here would silently fragment batches.
func SortContext(entries []ContextEntry) {
	for i := range entries {
		sort.Strings(entries[i].Values)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})
}

// Fingerprint is a stable identity for a context set.
//
// Two questions may share one SimulatePrincipalPolicy call only if their
// fingerprints match. The API applies ContextEntries per call, not per resource,
// so mixing two different context sets in one call would evaluate each resource
// under the other's conditions — a silent wrong answer rather than an error.
//
// Callers must SortContext first; Fingerprint does not sort, so that an
// unsorted set is a visible bug rather than a hidden normalisation.
func Fingerprint(entries []ContextEntry) string {
	if len(entries) == 0 {
		return "none"
	}
	h := sha256.New()
	for _, e := range entries {
		// Length prefixes keep {Key: "ab", Values: ["c"]} distinct from
		// {Key: "a", Values: ["bc"]}.
		writeField(h, e.Key)
		writeField(h, string(e.Type))
		for _, v := range e.Values {
			writeField(h, v)
		}
		_, _ = io.WriteString(h, ";")
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func writeField(w io.Writer, s string) {
	_, _ = io.WriteString(w, strconv.Itoa(len(s)))
	_, _ = io.WriteString(w, ":")
	_, _ = io.WriteString(w, s)
}

// ContextKeys returns just the key names, sorted, for reporting.
func ContextKeys(entries []ContextEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Key)
	}
	sort.Strings(out)
	return out
}

// EqualKey reports whether two condition-key names refer to the same key.
// IAM treats condition key names case-insensitively.
func EqualKey(a, b string) bool { return strings.EqualFold(a, b) }
