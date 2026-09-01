package simulate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/aws/smithy-go"
)

// PermissionError means OUR caller lacks a required IAM read permission.
//
// Categorically different from the simulation reporting that the SIMULATED
// principal is denied, and it must never be rendered as a finding: one is a
// result, the other is preflight being unable to produce results at all.
type PermissionError struct {
	Action       string
	PrincipalARN string
	Err          error
}

func (e *PermissionError) Error() string {
	return fmt.Sprintf("this identity is not allowed to call %s: %v", e.Action, e.Err)
}
func (e *PermissionError) Unwrap() error { return e.Err }

// PrincipalNotFoundError means PolicySourceArn does not resolve to a real
// principal.
//
// Measured (E8d): AWS fails loudly here rather than returning a misleading
// denial, so this is always a real configuration problem and never a finding.
type PrincipalNotFoundError struct {
	ARN string
	Err error
}

func (e *PrincipalNotFoundError) Error() string {
	return fmt.Sprintf("no such IAM principal: %s", e.ARN)
}
func (e *PrincipalNotFoundError) Unwrap() error { return e.Err }

// RequiredActions are the IAM actions preflight needs. Three, all read-only.
var RequiredActions = []string{
	"iam:SimulatePrincipalPolicy",
	"iam:GetContextKeysForPrincipalPolicy",
	"sts:GetCallerIdentity",
}

// RequiredPolicyJSON returns the exact policy to add.
//
// The two IAM actions are scoped to the principal being checked rather than "*".
// That is not pedantry: SimulatePrincipalPolicy discloses the permissions of
// whatever principal it is pointed at, so granting it account-wide hands the
// holder a way to enumerate every identity's access. A tool that asks for AWS
// credentials should ask for the smallest thing that works.
func RequiredPolicyJSON(principalARN string) string {
	resource := any(principalARN)
	if principalARN == "" {
		resource = "*"
	}
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Sid":    "PreflightSimulate",
				"Effect": "Allow",
				"Action": []string{
					"iam:SimulatePrincipalPolicy",
					"iam:GetContextKeysForPrincipalPolicy",
				},
				"Resource": resource,
			},
			map[string]any{
				"Sid":      "PreflightIdentity",
				"Effect":   "Allow",
				"Action":   "sts:GetCallerIdentity",
				"Resource": "*",
			},
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		// The document is a literal; marshalling it cannot fail in practice.
		return ""
	}
	return string(b)
}

// errKind classifies a failure into what the caller should do about it.
type errKind int

const (
	// errFatalPermission and errFatalPrincipal abort the run: no amount of
	// retrying or degrading produces a useful answer.
	errFatalPermission errKind = iota
	errFatalPrincipal
	// errBatch degrades only the affected batch. The run continues and the
	// affected findings become Unchecked.
	errBatchThrottled
	errBatchTransient
	errBatchInvalidInput
)

// apiErrorCode returns the AWS error code, or "" if err is not an API error.
func apiErrorCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// classifyErr maps a failure onto what to do about it.
func classifyErr(err error) errKind {
	switch code := apiErrorCode(err); code {
	case "AccessDenied", "AccessDeniedException", "UnauthorizedOperation":
		return errFatalPermission
	case "NoSuchEntity", "NoSuchEntityException":
		return errFatalPrincipal
	case "Throttling", "ThrottlingException", "RequestLimitExceeded",
		"TooManyRequestsException", "SlowDown", "RequestThrottled":
		return errBatchThrottled
	case "InvalidInput", "ValidationError", "ValidationException":
		// Almost always means an ARN we synthesised is malformed. Loud, but
		// batch-level: one bad ARN must not kill a 300-resource run.
		return errBatchInvalidInput
	case "ServiceFailure", "ServiceUnavailable", "InternalFailure", "InternalError":
		return errBatchTransient
	}

	// Not an API error. Context cancellation, timeouts and connection failures
	// are all transient from our point of view — they cost us results, not
	// correctness.
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errBatchTransient
	case errors.As(err, &netErr):
		return errBatchTransient
	}
	return errBatchTransient
}

// fatal converts an error into the caller-facing fatal type, or returns nil if
// the failure is one the run can absorb.
func fatal(err error, principalARN, action string) error {
	switch classifyErr(err) {
	case errFatalPermission:
		return &PermissionError{Action: action, PrincipalARN: principalARN, Err: err}
	case errFatalPrincipal:
		return &PrincipalNotFoundError{ARN: principalARN, Err: err}
	default:
		return nil
	}
}

// describeBatchErr renders a batch failure for the run-level warning list.
func describeBatchErr(err error, arns []string) string {
	switch classifyErr(err) {
	case errBatchThrottled:
		return fmt.Sprintf("throttled past retries; %d resource(s) went unevaluated: %v", len(arns), truncateList(arns))
	case errBatchInvalidInput:
		return fmt.Sprintf("AWS rejected a request as malformed, which usually means a synthesised ARN is wrong; "+
			"%d resource(s) went unevaluated: %v (%v)", len(arns), truncateList(arns), err)
	default:
		return fmt.Sprintf("%d resource(s) went unevaluated: %v (%v)", len(arns), truncateList(arns), err)
	}
}

// truncateList keeps a warning readable when a batch covered many resources.
func truncateList(items []string) string {
	const max = 3
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:max], ", "), len(items)-max)
}
