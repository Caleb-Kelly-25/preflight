// Package awsref reads AWS's machine-readable service reference and uses it to
// check that each mapped action can actually authorize against the resource
// type an entry's arn_format names.
//
// This closes the one correctness dimension nothing else covers.
// accessanalyzer:ValidatePolicy is authoritative for action NAMES, and measured
// 2026-09-25 it checks nothing else: a policy granting lambda:CreateFunction on
// an S3 bucket ARN produces zero findings of any severity, while a planted
// s3:PutBucketEncryption in the same document is caught. So every arn_format in
// the database has been unverified since the database began.
//
// Why that matters rather than being tidiness: a wrong arn_format usually
// over-reports, because a simulation scoped to the wrong ARN is denied and the
// false positive is visible. But when the templated ARN is BROADER than what
// AWS authorizes against, a policy allowing the broad form allows our query
// while denying the real call — a false pass, which is the one failure this
// project treats as unforgivable.
//
// The reference lives at
// https://servicereference.us-east-1.amazonaws.com/v1/<service>/<service>.json
// and needs no credentials. Everything in this file is pure; fetching is the
// caller's job, so the logic is testable offline.
package awsref

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Service is the subset of the reference document this package uses.
type Service struct {
	Name      string     `json:"Name"`
	Actions   []Action   `json:"Actions"`
	Resources []Resource `json:"Resources"`
}

// Action is one IAM action and the resource types it authorizes against.
type Action struct {
	Name string `json:"Name"`
	// Resources is null for actions that authorize only against "*" —
	// s3:ListAllMyBuckets, for instance. That is a meaningful state, not a
	// missing field, so it is distinguished from an empty list.
	Resources []struct {
		Name string `json:"Name"`
	} `json:"Resources"`
}

// Resource is one resource type and the ARN shapes it takes.
type Resource struct {
	Name       string   `json:"Name"`
	ARNFormats []string `json:"ARNFormats"`
}

// Parse reads a service reference document.
func Parse(r io.Reader) (*Service, error) {
	var s Service
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("decoding service reference: %w", err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("service reference has no Name field")
	}
	return &s, nil
}

// URL is where a service's reference document lives.
func URL(service string) string {
	return fmt.Sprintf("https://servicereference.us-east-1.amazonaws.com/v1/%s/%s.json", service, service)
}

// varPattern matches a ${Var} placeholder in either AWS's ARN formats or ours.
var varPattern = regexp.MustCompile(`\$\{[A-Za-z0-9_]+\}`)

// normalizeARN reduces an ARN template to its structure, so that two templates
// naming the same shape compare equal even when they name their variables
// differently. AWS writes ${BucketName} where an entry might write ${Bucket};
// the shape is what carries the meaning.
func normalizeARN(format string) string {
	return varPattern.ReplaceAllString(strings.TrimSpace(format), "${}")
}

// ResourceTypesFor returns the resource types an action authorizes against.
//
// known is false when the service reference does not list the action at all,
// which usually means a typo — but is reported separately from a mismatch
// because the two need different fixes.
//
// scopeless is true when the action exists but authorizes only against "*".
func (s *Service) ResourceTypesFor(action string) (types []string, scopeless, known bool) {
	name := strings.TrimPrefix(action, s.Name+":")
	for _, a := range s.Actions {
		if !strings.EqualFold(a.Name, name) {
			continue
		}
		if len(a.Resources) == 0 {
			return nil, true, true
		}
		for _, r := range a.Resources {
			types = append(types, r.Name)
		}
		return types, false, true
	}
	return nil, false, false
}

// isPassthrough reports whether a template is a bare variable, meaning the
// attribute it reads already holds a complete ARN. There is no structure to
// compare in that case — the shape is whatever the configuration supplies.
func isPassthrough(format string) bool {
	return normalizeARN(format) == "${}"
}

// ResourceTypesMatching returns the resource types whose ARN shape matches the
// given template. More than one can match: several EC2 types share a shape and
// differ only in the literal path segment, which normalizeARN preserves.
func (s *Service) ResourceTypesMatching(arnFormat string) []string {
	if arnFormat == "" {
		return nil
	}
	want := normalizeARN(arnFormat)
	var out []string
	for _, r := range s.Resources {
		for _, f := range r.ARNFormats {
			if normalizeARN(f) == want {
				out = append(out, r.Name)
				break
			}
		}
	}
	return out
}
