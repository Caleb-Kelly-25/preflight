// Package plan parses the JSON representation of a Terraform plan, as produced
// by `terraform show -json <planfile>`.
//
// Only the subset of the plan format that preflight actually needs is modelled
// here. The full schema is large and mostly irrelevant to permission analysis;
// decoding just the fields we use keeps us tolerant of unrelated format churn.
package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Action is a single change action Terraform intends to take on a resource.
type Action string

const (
	ActionNoOp   Action = "no-op"
	ActionCreate Action = "create"
	ActionRead   Action = "read"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
)

// SupportedFormatMajor is the plan-JSON major version this parser is written
// against. Terraform bumps the minor version for additive changes, which are
// safe to ignore, and the major version for breaking ones, which are not.
const SupportedFormatMajor = "1"

// Plan is a decoded `terraform show -json` document.
type Plan struct {
	FormatVersion    string           `json:"format_version"`
	TerraformVersion string           `json:"terraform_version"`
	ResourceChanges  []ResourceChange `json:"resource_changes"`

	// Configuration is decoded only far enough to find the AWS provider's
	// region, which aws:RequestedRegion must be supplied from. Everything else
	// in the configuration block is ignored.
	Configuration Configuration `json:"configuration"`
}

// Configuration is the subset of the plan's configuration block we read.
type Configuration struct {
	ProviderConfig map[string]ProviderConfig `json:"provider_config"`
}

// ProviderConfig is one provider block.
type ProviderConfig struct {
	Name        string                `json:"name"`
	Expressions map[string]ConfigExpr `json:"expressions"`
}

// ConfigExpr is a configuration expression. Only literal values are usable: a
// region computed from a variable is not knowable from the plan alone.
type ConfigExpr struct {
	ConstantValue any `json:"constant_value"`
}

// ProviderRegion returns the AWS provider's region when the configuration sets
// it to a literal.
//
// Sourcing the region matters more than it looks: leaving aws:RequestedRegion
// unsupplied lets the simulator substitute a fixed us-east-1 and return a
// confident "allowed" for a deploy that would fail elsewhere. See
// docs/DESIGN.md §7.3.
func (p *Plan) ProviderRegion() (string, bool) {
	for name, pc := range p.Configuration.ProviderConfig {
		if pc.Name != "aws" && name != "aws" && !strings.HasPrefix(name, "aws.") {
			continue
		}
		expr, ok := pc.Expressions["region"]
		if !ok {
			continue
		}
		if s, ok := expr.ConstantValue.(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// ResourceChange is one resource Terraform intends to act on.
type ResourceChange struct {
	Address       string `json:"address"`
	ModuleAddress string `json:"module_address,omitempty"`
	// Mode is "managed" for resources and "data" for data sources.
	Mode string `json:"mode"`
	// Type is the Terraform resource type, e.g. "aws_s3_bucket".
	Type         string `json:"type"`
	Name         string `json:"name"`
	ProviderName string `json:"provider_name"`
	Change       Change `json:"change"`
}

// Change describes the before/after state of a single resource.
type Change struct {
	Actions []Action       `json:"actions"`
	Before  map[string]any `json:"before"`
	After   map[string]any `json:"after"`
	// AfterUnknown marks attributes whose values Terraform cannot know until
	// apply time. This is load-bearing for permission analysis: an unknown
	// bucket name or ARN means we cannot build the resource ARN to simulate
	// against, and must degrade the confidence level accordingly.
	AfterUnknown map[string]any `json:"after_unknown"`
}

// Parse decodes a `terraform show -json` document.
func Parse(r io.Reader) (*Plan, error) {
	var p Plan
	dec := json.NewDecoder(r)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("decoding terraform plan JSON: %w", err)
	}
	if p.FormatVersion == "" {
		return nil, fmt.Errorf("missing format_version: input does not look like `terraform show -json` output")
	}
	return &p, nil
}

// UnsupportedFormat reports whether the plan's format version has a major
// version this parser was not written against. Callers should warn rather than
// hard-fail, so a Terraform upgrade degrades to a visible caveat instead of an
// outage.
func (p *Plan) UnsupportedFormat() bool {
	major, _, _ := strings.Cut(p.FormatVersion, ".")
	return major != SupportedFormatMajor
}

// IsReplace reports whether the change is a destroy-and-recreate. Replacements
// require both the delete and the create permissions, so they must not be
// collapsed into a single operation.
func (c Change) IsReplace() bool {
	var create, del bool
	for _, a := range c.Actions {
		switch a {
		case ActionCreate:
			create = true
		case ActionDelete:
			del = true
		}
	}
	return create && del
}

// Operations returns the distinct permission-relevant operations implied by the
// change, in a stable order. A no-op or read yields none: neither requires
// apply-time write permission.
func (c Change) Operations() []Action {
	var ops []Action
	for _, want := range []Action{ActionCreate, ActionUpdate, ActionDelete} {
		for _, got := range c.Actions {
			if got == want {
				ops = append(ops, want)
				break
			}
		}
	}
	return ops
}

// IsAWS reports whether the change belongs to the AWS provider. The provider
// name is a source address such as "registry.terraform.io/hashicorp/aws", so we
// match on the suffix rather than the whole string.
func (rc ResourceChange) IsAWS() bool {
	return strings.HasSuffix(rc.ProviderName, "/aws") || rc.ProviderName == "aws"
}

// Actionable reports whether the change needs a permission check at all.
func (rc ResourceChange) Actionable() bool {
	return rc.Mode == "managed" && rc.IsAWS() && len(rc.Change.Operations()) > 0
}

// Actionable returns the resource changes that require apply-time AWS
// permissions, skipping data sources, no-ops, and non-AWS providers.
func (p *Plan) Actionable() []ResourceChange {
	var out []ResourceChange
	for _, rc := range p.ResourceChanges {
		if rc.Actionable() {
			out = append(out, rc)
		}
	}
	return out
}
