package simulate

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/Caleb-Kelly-25/preflight/internal/finding"
)

// maxPages bounds pagination.
//
// A non-advancing Marker or a server-side loop would otherwise hang a CI job
// indefinitely, which is a far worse outcome than reporting Unchecked. With
// MaxItems at 1000 this ceiling is unreachable for any realistic batch.
const maxPages = 100

// runBatch issues one batch's calls and caches every pair returned.
//
// Results received before a mid-pagination failure are KEPT and cached; only the
// pairs never returned go unevaluated. A partial answer is more useful than
// discarding work AWS already did, provided the gap is reported rather than
// treated as a pass.
func (c *Client) runBatch(ctx context.Context, principalARN string, b batch) (received int, err error) {
	entries := toContextEntries(b.entries)
	arns := b.arns

	var marker *string
	for page := 0; page < maxPages; page++ {
		in := &iam.SimulatePrincipalPolicyInput{
			PolicySourceArn: aws.String(principalARN),
			ActionNames:     b.actions,
			ResourceArns:    arns,
			MaxItems:        aws.Int32(1000),
		}
		if len(entries) > 0 {
			in.ContextEntries = entries
		}
		if marker != nil {
			in.Marker = marker
		}

		out, callErr := c.iam.SimulatePrincipalPolicy(ctx, in)
		c.stats.addCall()
		if callErr != nil {
			return received, callErr
		}

		received += c.absorb(out.EvaluationResults, b)
		c.stats.addPage()

		if !out.IsTruncated || out.Marker == nil || *out.Marker == "" {
			return received, nil
		}
		// A Marker that does not advance means we would ask the same question
		// forever. Stop and report a partial rather than spin.
		if marker != nil && *marker == *out.Marker {
			return received, fmt.Errorf("pagination marker did not advance; stopped with a partial result")
		}
		marker = out.Marker
	}
	return received, fmt.Errorf("pagination exceeded %d pages; stopped with a partial result", maxPages)
}

// absorb caches every evaluation result, and reports how many were usable.
//
// Where the per-ARN answers live depends on how many ARNs were in the call, and
// getting this wrong is silently catastrophic. Measured 2026-09-01 against a
// real account, asking about two buckets under a policy that allows exactly one:
//
//	EvaluationResults[0].EvalResourceName = "arn:aws:s3:::${BucketName}/${KeyName}"
//	EvaluationResults[0].EvalDecision     = "implicitDeny"
//	ResourceSpecificResults[0]            = {exact-bucket,  allowed}
//	ResourceSpecificResults[1]            = {other-bucket,  implicitDeny}
//
// The top-level entry is an AGGREGATE with a templated resource name — reading
// its decision would report the allowed bucket as denied. The real per-ARN
// answers are in ResourceSpecificResults. With a single ARN the top level does
// carry the real ARN and the right decision, so both shapes must be handled.
func (c *Client) absorb(results []iamtypes.EvaluationResult, b batch) int {
	fp := finding.Fingerprint(b.entries)
	var n int

	store := func(action, arn string, d iamtypes.PolicyEvaluationDecisionType, missing []string) {
		c.cache.put(key{action: action, arn: arn, ctxFP: fp}, finding.ActionResult{
			Action:               action,
			ResourceARN:          arn,
			Decision:             toDecision(d),
			MissingContextValues: missing,
		})
		n++
	}

	for _, r := range results {
		if r.EvalActionName == nil {
			continue
		}
		action := *r.EvalActionName

		// Correlate results back to the ARNs we sent. AWS may echo a templated
		// name rather than ours, so match exactly, then case-insensitively, and
		// otherwise refuse to guess: a wrong attribution is a confident wrong
		// answer, while an unmatched pair merely degrades to Unchecked.
		if len(r.ResourceSpecificResults) > 0 {
			for _, rsr := range r.ResourceSpecificResults {
				arn, ok := matchARN(rsr.EvalResourceName, b.arns)
				if !ok {
					continue
				}
				store(action, arn, rsr.EvalResourceDecision, rsr.MissingContextValues)
			}
			continue
		}

		arn, ok := matchARN(r.EvalResourceName, b.arns)
		if !ok {
			continue
		}
		store(action, arn, r.EvalDecision, r.MissingContextValues)
	}

	c.stats.addEvaluations(n)
	return n
}

// matchARN maps an echoed resource name back to the ARN we asked about.
func matchARN(echoed *string, sent []string) (string, bool) {
	if echoed == nil {
		// With a single ARN in the call there is no ambiguity to resolve.
		if len(sent) == 1 {
			return sent[0], true
		}
		return "", false
	}
	for _, a := range sent {
		if a == *echoed {
			return a, true
		}
	}
	for _, a := range sent {
		if strings.EqualFold(a, *echoed) {
			return a, true
		}
	}
	return "", false
}

// toDecision maps the SDK enum onto ours. An unrecognised value is treated as
// not simulated rather than guessed at: a new decision type we do not
// understand must not be silently read as an allow.
func toDecision(d iamtypes.PolicyEvaluationDecisionType) finding.Decision {
	switch d {
	case iamtypes.PolicyEvaluationDecisionTypeAllowed:
		return finding.DecisionAllowed
	case iamtypes.PolicyEvaluationDecisionTypeExplicitDeny:
		return finding.DecisionExplicitDeny
	case iamtypes.PolicyEvaluationDecisionTypeImplicitDeny:
		return finding.DecisionImplicitDeny
	default:
		return finding.DecisionNotSimulated
	}
}

// toContextEntries converts our context entries into the SDK's shape.
func toContextEntries(entries []finding.ContextEntry) []iamtypes.ContextEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]iamtypes.ContextEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, iamtypes.ContextEntry{
			ContextKeyName:   aws.String(e.Key),
			ContextKeyType:   iamtypes.ContextKeyTypeEnum(e.Type),
			ContextKeyValues: e.Values,
		})
	}
	return out
}
