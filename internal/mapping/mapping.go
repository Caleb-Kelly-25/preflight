// Package mapping loads the resource-type-to-IAM-action database.
//
// The database itself lives at the repository root under mappings/, deliberately
// outside internal/ — it is a community-maintained asset, not an implementation
// detail, and contributors should not have to read Go to fix an entry.
package mapping

import (
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// Operation is a permission-relevant lifecycle operation on a resource.
type Operation string

// Action is one IAM action, optionally required only under a condition.
//
// It unmarshals from either a bare string or a mapping, so the flat v1 form
// stays valid:
//
//	create: [iam:CreateRole]
//	create:
//	  - action: iam:CreateRole
//	  - action: iam:TagRole
//	    when: { attribute_set: tags }
//
// Keeping the simple form simple matters for contribution volume: most entries
// need no conditions at all, and requiring the verbose shape everywhere would
// tax every contributor to serve a minority of entries.
type Action struct {
	Action string     `yaml:"action"`
	When   *Condition `yaml:"when,omitempty"`
}

// Condition narrows when an action is actually required.
//
// Deliberately just two forms, and deliberately not an expression language: a
// contributor has to be able to check an entry by eye, and a reviewer has to be
// able to tell whether it is right. Anything richer defeats the point of the
// database being open and inspectable.
type Condition struct {
	// AttributeSet requires the action only when the named attribute has a
	// value. An attribute that is configured but unknown until apply counts as
	// set — it will have a value, we just cannot see it yet.
	AttributeSet string `yaml:"attribute_set,omitempty"`
	// AttributeChanged requires the action only when any named attribute
	// differs between the prior and planned state.
	AttributeChanged []string `yaml:"attribute_changed,omitempty"`
}

// Reference is an action required against another resource's ARN.
type Reference struct {
	Action string `yaml:"action"`
	// ARNFrom names the attribute holding the referenced resource. Its value is
	// used as the ARN directly unless ARNFormat is given.
	ARNFrom string `yaml:"arn_from"`
	// ARNFormat builds an ARN when the attribute holds a bare name rather than
	// an ARN — aws_iam_instance_profile.role is a role *name*, while
	// aws_lambda_function.role is already an ARN. ${Name} is the attribute's
	// value; ${Partition}, ${Account} and ${Region} come from the caller.
	ARNFormat string `yaml:"arn_format,omitempty"`
	// Operations limits which operations need this. Empty means all of them.
	// Deleting an instance profile removes the role rather than passing it, so
	// scoping matters.
	Operations []Operation `yaml:"operations,omitempty"`
	When       *Condition  `yaml:"when,omitempty"`
}

// AppliesTo reports whether this reference is required for an operation.
func (ref Reference) AppliesTo(op Operation) bool {
	if len(ref.Operations) == 0 {
		return true
	}
	for _, o := range ref.Operations {
		if o == op {
			return true
		}
	}
	return false
}

// UnmarshalYAML accepts the flat string form as sugar for {action: X}.
func (a *Action) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&a.Action)
	}
	// A distinct type, or this recurses into itself.
	type plain Action
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*a = Action(p)
	return nil
}

const (
	OpCreate Operation = "create"
	OpUpdate Operation = "update"
	OpDelete Operation = "delete"
)

// Status marks how much a mapping entry can be trusted. Entries start as
// StatusDraft and only become StatusVerified once checked against AWS's own
// service-authorization reference; the confidence model depends on this
// distinction being honest.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusVerified Status = "verified"
)

// Resource is one resource type's mapping.
type Resource struct {
	// Type is the Terraform resource type, e.g. "aws_s3_bucket".
	Type string `yaml:"type"`
	// Service is the AWS service prefix used in IAM actions, e.g. "s3".
	Service string `yaml:"service"`
	// ARNFormat is the ARN template for this resource type, using ${Var}
	// placeholders, e.g. "arn:${Partition}:s3:::${BucketName}".
	ARNFormat string `yaml:"arn_format"`
	// ARNAttributes maps each ${Var} in ARNFormat to the Terraform attribute
	// that supplies it. A variable with no entry here cannot be filled from the
	// plan, which forces a wildcard ARN and a downgraded confidence level.
	ARNAttributes map[string]string `yaml:"arn_attributes,omitempty"`

	// ARNPrefixAttributes maps an ARN variable to a name_prefix attribute, used
	// when the real name is generated at apply time. See BuildARN for why a
	// representative name beats "*" here.
	ARNPrefixAttributes map[string]string `yaml:"arn_prefix_attributes,omitempty"`
	// Operations lists the IAM actions each lifecycle operation requires.
	Operations map[Operation][]Action `yaml:"operations"`

	// ReadActions are required by EVERY operation, not one of them.
	//
	// Terraform reads a resource back after writing it, so an apply needs the
	// provider's whole read set on top of the write action. Measured
	// 2026-09-01: creating a tagged aws_s3_bucket needs 17 actions, of which 14
	// are reads — and omitting s3:ListBucket does not fail cleanly, it makes the
	// provider retry HeadBucket until the apply times out.
	//
	// These are unioned into every operation's action list, so an entry states
	// them once rather than repeating them three times.
	ReadActions []string `yaml:"read_actions,omitempty"`
	// ResourcePolicyCapable lists the operations during which the target
	// resource can be denied by its own resource-based policy, which simulation
	// cannot evaluate for roles.
	//
	// This is per-operation, not per-type, because a resource that does not yet
	// exist has no policy to deny it. Measured 2026-09-01 against a bucket whose
	// policy denied the calling role every s3 action:
	//
	//   PutBucketVersioning on that bucket  -> denied by the resource policy
	//   GetBucketTagging on that bucket     -> denied by the resource policy
	//   CreateBucket for a NEW bucket       -> allowed
	//   read-backs on the new bucket        -> allowed
	//
	// So aws_s3_bucket lists [update, delete]: creating it cannot be denied by
	// a policy that does not exist yet. But aws_s3_bucket_versioning lists
	// create too, because its "create" writes to a bucket that already exists —
	// the Terraform operation and the AWS resource's lifetime are not the same
	// thing, and only the entry's author knows which is which.
	ResourcePolicyCapable []Operation `yaml:"resource_policy_capable,omitempty"`

	// ContextKeys sources condition-key values from plan attributes.
	//
	// A key ending in "/*" matches any key with that prefix, and the tail after
	// the prefix indexes into `from`, which must then be a map:
	// aws:RequestTag/Env reads tags["Env"]. That single glob form is the only
	// pattern supported — anything richer stops contributors being able to
	// check an entry by eye.
	ContextKeys map[string]ContextKeySource `yaml:"context_keys,omitempty"`

	// References are actions this resource needs on a DIFFERENT resource that it
	// points at. iam:PassRole is the motivating case and the most commonly
	// missed permission in real Terraform deploys: handing a role to a service
	// is authorised against the *role's* ARN, so the permission belongs to
	// whichever resource does the handing, not to the role.
	//
	// A per-resource, single-ARN schema cannot express that at all, which is why
	// every entry that needs it previously carried a note apologising instead.
	References []Reference `yaml:"references,omitempty"`

	Status Status `yaml:"status"`

	// VerifiedOperations lists operations proven complete when the entry as a
	// whole is not.
	//
	// Verification is empirical and per-operation: proving that a create
	// succeeds with exactly the mapped actions says nothing about update, whose
	// required actions depend on which attributes changed. Without this field
	// the only honest status for a partly-proven entry is `draft`, which
	// discards real work — aws_iam_role's create and delete were established
	// against provider v5.100.0 and then had to be recorded as unproven because
	// update was not.
	//
	// Only meaningful with `status: draft`; `status: verified` already means
	// every operation is verified.
	VerifiedOperations []Operation `yaml:"verified_operations,omitempty"`

	Notes string `yaml:"notes,omitempty"`
	// Source is where the action list was derived from, so a reviewer can
	// re-check it.
	Source string `yaml:"source,omitempty"`
}

// file is the on-disk shape of one mappings/*.yaml document.
type file struct {
	Resources []Resource `yaml:"resources"`
}

// Database is an in-memory index of the mapping files.
type Database struct {
	byType map[string]Resource
}

// Load reads every *.yaml file in fsys and indexes it by resource type.
func Load(fsys fs.FS) (*Database, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("reading mapping directory: %w", err)
	}

	db := &Database{byType: make(map[string]Resource)}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || path.Ext(name) != ".yaml" {
			continue
		}
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		var f file
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		for _, r := range f.Resources {
			if err := r.validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if _, dup := db.byType[r.Type]; dup {
				return nil, fmt.Errorf("%s: duplicate mapping for resource type %q", name, r.Type)
			}
			db.byType[r.Type] = r
		}
	}
	return db, nil
}

func (r Resource) validate() error {
	if r.Type == "" {
		return fmt.Errorf("mapping entry is missing `type`")
	}
	if r.Service == "" {
		return fmt.Errorf("%s: missing `service`", r.Type)
	}
	if len(r.Operations) == 0 {
		return fmt.Errorf("%s: no operations defined", r.Type)
	}
	if r.Status != StatusDraft && r.Status != StatusVerified {
		return fmt.Errorf("%s: status must be %q or %q, got %q", r.Type, StatusDraft, StatusVerified, r.Status)
	}
	// `verified` is the strongest claim this database makes: it is what lets a
	// finding reach Verified rather than being capped at Likely. A claim with
	// nothing behind it is worse than no claim, so the reviewer's starting point
	// is required rather than merely conventional.
	if (r.Status == StatusVerified || len(r.VerifiedOperations) > 0) && strings.TrimSpace(r.Source) == "" {
		return fmt.Errorf("%s: claiming verification requires a `source` URL recording how the action list was established", r.Type)
	}
	if r.Status == StatusVerified && len(r.VerifiedOperations) > 0 {
		return fmt.Errorf("%s: `verified_operations` is redundant with status %q, which already covers every operation", r.Type, StatusVerified)
	}
	// A prefix attribute keyed to a variable the ARN template does not use is a
	// typo that would silently never apply, leaving the ARN at "*" forever.
	if len(r.ARNPrefixAttributes) > 0 {
		vars := make(map[string]bool)
		for _, v := range r.ARNVars() {
			vars[v] = true
		}
		for v := range r.ARNPrefixAttributes {
			if !vars[v] {
				return fmt.Errorf("%s: arn_prefix_attributes names ${%s}, which arn_format does not use", r.Type, v)
			}
		}
	}
	for _, ref := range r.References {
		if !strings.Contains(ref.Action, ":") {
			return fmt.Errorf("%s: reference action %q is not of the form service:Action", r.Type, ref.Action)
		}
		if ref.ARNFrom == "" {
			return fmt.Errorf("%s: reference %q has no `arn_from`; without it there is no resource to check against", r.Type, ref.Action)
		}
		for _, op := range ref.Operations {
			switch op {
			case OpCreate, OpUpdate, OpDelete:
			default:
				return fmt.Errorf("%s: reference %q lists unknown operation %q", r.Type, ref.Action, op)
			}
		}
	}
	for _, op := range r.VerifiedOperations {
		switch op {
		case OpCreate, OpUpdate, OpDelete:
		default:
			return fmt.Errorf("%s: verified_operations lists unknown operation %q", r.Type, op)
		}
		// Claiming an operation is verified when the entry does not map it at
		// all would be a claim about nothing.
		if len(r.Operations[op]) == 0 {
			return fmt.Errorf("%s: verified_operations lists %q, which the entry does not map", r.Type, op)
		}
	}
	for op, actions := range r.Operations {
		switch op {
		case OpCreate, OpUpdate, OpDelete:
		default:
			return fmt.Errorf("%s: unknown operation %q", r.Type, op)
		}
		for _, a := range actions {
			if !strings.Contains(a.Action, ":") {
				return fmt.Errorf("%s: action %q is not of the form service:Action", r.Type, a.Action)
			}
			if a.When == nil {
				continue
			}
			if a.When.AttributeSet == "" && len(a.When.AttributeChanged) == 0 {
				return fmt.Errorf("%s: action %q has an empty `when`; omit it if the action is always required", r.Type, a.Action)
			}
			if a.When.AttributeSet != "" && len(a.When.AttributeChanged) > 0 {
				return fmt.Errorf("%s: action %q sets both `attribute_set` and `attribute_changed`; use one", r.Type, a.Action)
			}
		}
	}
	for _, a := range r.ReadActions {
		if !strings.Contains(a, ":") {
			return fmt.Errorf("%s: read action %q is not of the form service:Action", r.Type, a)
		}
	}
	for _, op := range r.ResourcePolicyCapable {
		switch op {
		case OpCreate, OpUpdate, OpDelete:
		default:
			return fmt.Errorf("%s: resource_policy_capable lists unknown operation %q", r.Type, op)
		}
	}
	for key, src := range r.ContextKeys {
		if !strings.Contains(key, ":") {
			return fmt.Errorf("%s: context key %q is not of the form service:KeyName", r.Type, key)
		}
		if i := strings.Index(key, "*"); i >= 0 && i != len(key)-1 {
			return fmt.Errorf("%s: context key %q may only use * as a trailing wildcard", r.Type, key)
		}
		if src.From == "" {
			return fmt.Errorf("%s: context key %q has no `from` attribute", r.Type, key)
		}
		if src.Type != "" && !finding.ValidContextValueType(src.Type) {
			return fmt.Errorf("%s: context key %q has unknown type %q", r.Type, key, src.Type)
		}
	}
	return nil
}

// rcpServices are the AWS services governed by AWS Organizations resource
// control policies. SimulatePrincipalPolicy evaluates SCPs but explicitly does
// not support RCPs, so a resource in one of these services can never reach
// Verified.
//
// An earlier version of this list held seven services and noted that every one
// was also resource-policy-capable, so the RCP caveat "rode along" with the
// resource-policy one. That is no longer true in either direction, and both
// halves mattered:
//
//   - resource_policy_capable is now per-operation, so on a create the RCP
//     caveat stands alone rather than accompanying anything.
//   - AWS has since extended RCPs to services that carry no resource policy of
//     their own (dynamodb, logs, autoscaling and others below). Those would
//     have reached Verified with an RCP still able to deny them.
//
// Note the asymmetry with resource-based policies, which is why the "the
// resource does not exist yet" reasoning does NOT transfer here: a
// resource-based policy lives on the resource, so a resource that does not
// exist has none — but an RCP is attached to an account, OU, or organization
// root, so it exists and applies to the request that creates the resource.
// AWS: "RCPs apply to the resources that are authorized as part of an operation
// request... found in the Resource type column of the Action table." Creates
// authorize a resource type, so RCPs govern them.
//
// Checked against the service list on 2026-09-01. This list will drift; it is
// the kind of thing the mapping-maintenance pipeline should re-check.
//
// Source: https://docs.aws.amazon.com/organizations/latest/userguide/orgs_manage_policies_rcps.html
var rcpServices = map[string]bool{
	"aoss":                  true, // OpenSearch Serverless
	"appconfig":             true,
	"appstream":             true,
	"autoscaling":           true,
	"cloudfront":            true,
	"cloudsearch":           true,
	"codebuild":             true,
	"codecommit":            true,
	"codepipeline":          true,
	"cognito-identity":      true,
	"cognito-idp":           true,
	"comprehend":            true,
	"comprehendmedical":     true,
	"cost-optimization-hub": true,
	"dax":                   true,
	"dynamodb":              true,
	"ecr":                   true,
	"events":                true, // EventBridge
	"firehose":              true,
	"fis":                   true,
	"health":                true,
	"inspector-scan":        true,
	"kendra":                true,
	"kinesisvideo":          true,
	"kms":                   true,
	"logs":                  true, // CloudWatch Logs
	"memorydb":              true,
	"networkmonitor":        true,
	"opensearch":            true,
	"pca-connector-ad":      true,
	"polly":                 true,
	"pricing":               true,
	"s3":                    true,
	"secretsmanager":        true,
	"signin":                true,
	"sqs":                   true,
	"sts":                   true,
	"support":               true,
	"textract":              true,
	"timestream-influxdb":   true,
	"transcribe":            true,
	"transfer":              true,
	"translate":             true,
	"wafv2":                 true,
}

// RCPGoverned reports whether this resource's service is subject to resource
// control policies.
func (r Resource) RCPGoverned() bool { return rcpServices[r.Service] }

// ResourcePolicyApplies reports whether a resource-based policy could deny this
// operation. See the ResourcePolicyCapable field for the measurement behind the
// per-operation distinction.
func (r Resource) ResourcePolicyApplies(op Operation) bool {
	for _, o := range r.ResourcePolicyCapable {
		if o == op {
			return true
		}
	}
	return false
}

// Verified reports whether every operation on the entry is proven.
func (r Resource) Verified() bool { return r.Status == StatusVerified }

// VerifiedFor reports whether this specific operation is proven, either because
// the whole entry is verified or because the operation is listed individually.
// An unproven operation caps its findings at Likely.
func (r Resource) VerifiedFor(op Operation) bool {
	if r.Verified() {
		return true
	}
	for _, o := range r.VerifiedOperations {
		if o == op {
			return true
		}
	}
	return false
}

// Lookup returns the mapping for a Terraform resource type.
func (d *Database) Lookup(resourceType string) (Resource, bool) {
	r, ok := d.byType[resourceType]
	return r, ok
}

// Actions returns the IAM actions required for one operation on one resource
// type. The second return value is false when the type is unmapped, which the
// caller must surface as Unchecked rather than as a pass.
func (d *Database) Actions(resourceType string, op Operation) ([]Action, bool) {
	r, ok := d.byType[resourceType]
	if !ok {
		return nil, false
	}
	return r.Operations[op], true
}

// RequiredActions returns the actions this operation needs for a resource with
// these attributes, dropping conditional actions whose condition does not hold.
//
// `attrs` is the state the operation acts on (planned for create and update,
// prior for delete) and `unknown` marks attributes whose values are not known
// until apply. `before` and `after` are both needed for attribute_changed.
//
// Where a condition cannot be decided, the action is kept. That over-reports,
// which produces a visible false positive rather than a silent false pass.
func (r Resource) RequiredActions(op Operation, attrs, unknown, before, after map[string]any) []string {
	acts := r.Operations[op]
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		if conditionHolds(a.When, attrs, unknown, before, after) {
			out = append(out, a.Action)
		}
	}
	return out
}

func conditionHolds(c *Condition, attrs, unknown, before, after map[string]any) bool {
	if c == nil {
		return true
	}
	if c.AttributeSet != "" {
		// Configured but unknown until apply still means it will have a value.
		if _, ok := unknown[c.AttributeSet]; ok {
			return true
		}
		return isSet(attrs[c.AttributeSet])
	}
	for _, name := range c.AttributeChanged {
		if _, ok := unknown[name]; ok {
			return true // cannot tell; assume it changed
		}
		if !reflect.DeepEqual(before[name], after[name]) {
			return true
		}
	}
	return false
}

// isSet reports whether an attribute carries a meaningful value. Terraform
// renders unset attributes as null, and empty maps and lists are how "no tags"
// and "no rules" arrive, so neither counts as set.
func isSet(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	case bool:
		return t
	default:
		return true
	}
}

// Types lists every mapped resource type, sorted.
func (d *Database) Types() []string {
	out := make([]string, 0, len(d.byType))
	for t := range d.byType {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Len reports how many resource types are mapped.
func (d *Database) Len() int { return len(d.byType) }
