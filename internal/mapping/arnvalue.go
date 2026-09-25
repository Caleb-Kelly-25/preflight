package mapping

import (
	"fmt"
	"strings"
)

// ARNOrName declares that a Terraform attribute may hold EITHER a bare resource
// name OR a full ARN for the same resource.
//
// Several attributes are polymorphic in exactly this way and the schema could
// not say so, which cost two entries their ARN scoping entirely:
//
//	aws_lambda_permission.function_name  -> a name, or a function ARN
//	aws_ecs_service.cluster              -> a name, or a cluster ARN
//
// Both had to omit arn_format and fall back to "*", because substitution is
// unconditional: templating a value that was already an ARN nests one ARN
// inside another, producing a string that looks like an ARN, matches no policy
// ever written, and is reported with full confidence. A wildcard was the honest
// answer, and a bad one.
//
// The declaration is what makes the value safe to interpret. `service` and
// `resource_type` are both required because they are the only thing separating
// "this is the ARN I expected" from "this is some other ARN": without
// resource_type, arn:aws:iam::1:user/x and arn:aws:iam::1:role/x are the same
// value, and building role/x out of a user ARN is a confident wrong answer of
// precisely the kind this field exists to prevent.
type ARNOrName struct {
	// Service is the ARN service segment the value must carry when it is an
	// ARN, e.g. "lambda".
	Service string `yaml:"service"`
	// ResourceType is the resource-type segment it must carry, e.g. "function"
	// in `function:my-fn` or "cluster" in `cluster/prod`.
	ResourceType string `yaml:"resource_type"`
}

func (d ARNOrName) validate() error {
	if d.Service == "" {
		return fmt.Errorf("`arn_or_name` needs `service`; without it any ARN at all would be accepted, including one naming a different service")
	}
	if d.ResourceType == "" {
		return fmt.Errorf("`arn_or_name` needs `resource_type`; `role/x` and `user/x` are the same value without it, and building one out of the other is a confident wrong ARN")
	}
	return nil
}

// arnValueKind is the result of inspecting a polymorphic attribute value.
//
// It is deliberately THREE-valued. A two-way "is this an ARN?" test fails in
// the dangerous direction: anything it decides is "not an ARN" gets templated,
// so every value the parser merely fails to understand — a partial Lambda ARN
// like `123456789012:function:foo`, a qualified name like `my-fn:PROD` — would
// be built into a confidently wrong ARN. The third state exists so that
// "cannot tell" degrades to "*" instead of guessing.
type arnValueKind int

const (
	// valueAmbiguous means the value is neither a plain name nor an ARN we
	// recognise. The only safe answer is "*" with exact=false.
	valueAmbiguous arnValueKind = iota
	// valueName means a bare resource name, safe to substitute into a template.
	valueName
	// valueARN means a full ARN of exactly the declared service and resource
	// type.
	valueARN
)

// parsedARN is an ARN split into its six fields, with the resource portion
// further split into its type and id.
type parsedARN struct {
	Partition    string
	Service      string
	Region       string
	Account      string
	ResourceType string
	ResourceID   string
}

// classify decides what a polymorphic attribute value actually is.
//
// The rule that carries the safety: a bare resource name cannot contain a
// colon. Every value that does is either an ARN or something we do not
// understand, and both go to the parser — never down the "template it as a
// name" path. That single line is what stops a partial ARN or an alias-qualified
// function name from being nested inside a synthetic ARN.
func (d ARNOrName) classify(v string) (arnValueKind, parsedARN) {
	if v == "" {
		return valueAmbiguous, parsedARN{}
	}

	if !strings.ContainsAny(v, ":/") {
		// No separator of any kind: a plain name. A slash is excluded for the
		// same reason as a colon — `cluster/prod` is somebody pasting the
		// resource half of an ARN, and templating it yields
		// `service/cluster/prod/svc`, which is wrong with full confidence.
		return valueName, parsedARN{}
	}

	p, ok := parseARN(v)
	if !ok {
		return valueAmbiguous, parsedARN{}
	}
	// An ARN for the WRONG SERVICE is rejected, not accepted.
	//
	// The tempting alternative is to pass it through untouched on the grounds
	// that it is at least a real ARN. It is the wrong call twice over. First,
	// it is evidence that the premise is broken — either the mapping's
	// declaration is wrong or the configuration is doing something this entry
	// does not model — and building on a broken premise is how a confident
	// wrong answer is produced. Second, it converts a modelling error into a
	// false positive: lambda.yaml records the measurement that a policy
	// granting `lambda:CreateFunction` on an S3 ARN is accepted silently by
	// ValidatePolicy but produces an implicit deny at simulation time. Passing
	// `arn:aws:s3:::b` into a lambda check would therefore report a missing
	// permission the caller actually holds. Degrading to "*" says "not
	// checked", which is true.
	if !strings.EqualFold(p.Service, d.Service) {
		return valueAmbiguous, parsedARN{}
	}
	if !strings.EqualFold(p.ResourceType, d.ResourceType) {
		return valueAmbiguous, parsedARN{}
	}
	// The id is what gets substituted into a template, so it has to be a plain
	// name too. `function:my-fn:PROD` parses with id "my-fn:PROD" — a version
	// or alias qualifier, which is a different ARN from the unqualified
	// function and would not match a policy scoped to either one reliably.
	if p.ResourceID == "" || strings.ContainsAny(p.ResourceID, ":/") {
		return valueAmbiguous, parsedARN{}
	}
	return valueARN, p
}

// parseARN validates and splits an ARN:
// arn:<partition>:<service>:<region>:<account>:<resource>.
//
// Every segment is checked rather than merely counted, because the whole point
// of this function is to be unfoolable by a value that happens to contain
// colons. Failing here is safe — the caller degrades to "*" — so the checks are
// deliberately strict.
func parseARN(v string) (parsedARN, bool) {
	// Six fields exactly: the resource portion keeps any colons it contains,
	// which is how `function:my-fn` survives.
	fields := strings.SplitN(v, ":", 6)
	if len(fields) != 6 || fields[0] != "arn" {
		return parsedARN{}, false
	}

	p := parsedARN{
		Partition: fields[1],
		Service:   fields[2],
		Region:    fields[3],
		Account:   fields[4],
	}
	if p.Partition == "" || !isSegment(p.Partition) {
		return parsedARN{}, false
	}
	if p.Service == "" || !isSegment(p.Service) {
		return parsedARN{}, false
	}
	// The region is legitimately empty in global-service ARNs (IAM, S3).
	if p.Region != "" && !isSegment(p.Region) {
		return parsedARN{}, false
	}
	// Three spellings of the account field are real: empty (S3 buckets, IAM
	// global ARNs), twelve digits, and the literal "aws" (AWS-managed IAM
	// policies). Anything else is not an account we recognise — and since this
	// value can be adopted into a rebuilt ARN, an account we do not recognise
	// is one we decline to build with.
	if !isAccount(p.Account) {
		return parsedARN{}, false
	}

	resource := fields[5]
	if resource == "" {
		return parsedARN{}, false
	}
	// AWS spells the type/id boundary with either separator: `cluster/prod`
	// and `function:my-fn` are both ordinary. Whichever comes first wins.
	if i := strings.IndexAny(resource, "/:"); i >= 0 {
		p.ResourceType, p.ResourceID = resource[:i], resource[i+1:]
	} else {
		// A type-less ARN such as arn:aws:s3:::my-bucket. It can never satisfy
		// a declaration, since resource_type is required, but parsing it is
		// still correct.
		p.ResourceID = resource
	}
	return p, true
}

// isSegment reports whether an ARN segment is a plain token. Partitions,
// services and regions are all drawn from letters, digits, dots and hyphens
// (`aws-us-gov`, `cognito-idp`, `us-east-1`).
func isSegment(s string) bool {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

func isAccount(s string) bool {
	if s == "" || s == "aws" {
		return true
	}
	if len(s) != 12 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
