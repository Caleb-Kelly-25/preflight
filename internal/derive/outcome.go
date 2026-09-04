// Package derive establishes empirically which IAM actions a Terraform resource
// type actually requires.
//
// The mapping database's `verified` status means two things (mappings/README.md):
// that action names are correct, and that the list is complete. The second is
// the one that matters, because a missing action produces a false "allowed" for
// a deploy that will fail — and it can only be established by granting a role
// exactly the mapped actions, running a real apply, and removing actions one at
// a time to see which are load-bearing.
//
// That loop has been run by hand three times. Each run found the mapping wrong:
// aws_s3_bucket by 14 actions, aws_iam_role by 5, aws_vpc by one over-report.
// This package is that loop, so it can be run affordably and repeatably.
//
// Everything here is pure. AWS and Terraform reach it only through the Grantor
// and Applier interfaces, so the logic that decides what is required is testable
// with no credentials — the same rule the engine follows.
package derive

import "time"

// OutcomeKind is what one apply attempt told us.
type OutcomeKind int

const (
	// OutcomeUnknown is the zero value on purpose. A forgotten branch, an
	// unparsed result, or a struct built from nothing must never read as a
	// successful apply — the same invariant finding.Level enforces for
	// Verified, for the same reason. TestZeroOutcomeIsNotSuccess pins it.
	OutcomeUnknown OutcomeKind = iota

	// OutcomeSuccess: the apply completed. The granted set is sufficient for
	// this fixture's configuration.
	OutcomeSuccess

	// OutcomeDenied: an IAM denial we could attribute to at least one action.
	OutcomeDenied

	// OutcomeStalled: no forward progress before the budget expired. This is a
	// HANG, and it is deliberately not folded into OutcomeFailed.
	//
	// Omitting s3:ListBucket does not fail a bucket create — the provider
	// retries HeadBucket indefinitely. The first manual run burned ten minutes
	// and orphaned a bucket. During discovery a stall cannot be attributed to
	// an action; during minimisation it is positive evidence that the removed
	// action was load-bearing.
	OutcomeStalled

	// OutcomeOpaque: denied, but no action name could be parsed out. Some EC2
	// denials arrive as UnauthorizedOperation with only an encoded message.
	OutcomeOpaque

	// OutcomeFailed: the apply failed for a reason that is not an IAM denial —
	// a bad fixture, a quota, a capacity error.
	OutcomeFailed
)

func (k OutcomeKind) String() string {
	switch k {
	case OutcomeSuccess:
		return "success"
	case OutcomeDenied:
		return "denied"
	case OutcomeStalled:
		return "stalled"
	case OutcomeOpaque:
		return "opaque-denial"
	case OutcomeFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Outcome is the result of one apply attempt.
type Outcome struct {
	Kind          OutcomeKind
	DeniedActions []string // populated for OutcomeDenied; Terraform parallelises, so possibly several
	StalledAt     string   // resource address, when known
	Elapsed       time.Duration
	Detail        string // the provider's own message, for the audit trail
}

// Confidence records how well a claim was established. Like OutcomeKind, the
// zero value is the one that claims nothing.
type Confidence int

const (
	ConfidenceUnknown Confidence = iota
	ConfidenceInconclusive
	ConfidencePartial
	ConfidenceProven
)

func (c Confidence) String() string {
	switch c {
	case ConfidenceInconclusive:
		return "inconclusive"
	case ConfidencePartial:
		return "partial"
	case ConfidenceProven:
		return "proven"
	default:
		return "unknown"
	}
}

// Evidence records why one action is believed load-bearing.
type Evidence struct {
	Action  string
	Kind    OutcomeKind // Denied is a clean proof; Stalled means it hangs instead
	Detail  string
	Attempt int
}

// Attempt is one apply, kept so a result can be audited rather than trusted.
type Attempt struct {
	Index   int
	Granted []string
	Outcome Outcome
}
