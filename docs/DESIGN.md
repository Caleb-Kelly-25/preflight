# preflight — MVP Design

**Status:** design, pre-implementation. Supersedes the open-questions section of
[ARCHITECTURE.md](ARCHITECTURE.md).
**Companions:** [SPECS.md](../SPECS.md) (what to build), [GENERAL_DIRECTION.md](../GENERAL_DIRECTION.md) (why).

This document is what we build from. It resolves the questions the spec left
open, records the reasoning, and names what is still genuinely unknown.

---

## 0. Read this first: two findings that change the plan

Before designing anything I verified the `iam:SimulatePrincipalPolicy` contract
against current AWS documentation. Two things came back that the spec was
written without.

### 0.1 SCPs are now evaluated by the simulator

SPECS §5 defines the **Likely** state on the premise that "AWS Organizations
SCPs and/or resource-based policies are not reliably simulated by the underlying
AWS API." For SCPs, that is no longer true. Current AWS documentation states:

> The policy simulator evaluates SCPs, including condition keys and resource
> scoping in deny statements, but does not support resource control policies
> (RCPs).

and

> Because these keys are populated for you, SCPs and other policies that use
> principal or organization conditions are evaluated correctly.

The simulator populates `aws:PrincipalOrgID`, `aws:PrincipalOrgMasterAccountId`,
and `aws:PrincipalOrgPaths` automatically, and evaluates in-scope SCPs
server-side with **no additional caller permissions** — the CLI/API path needs
only `iam:SimulatePrincipalPolicy`.

**Consequences, in order of importance:**

1. **The free tier gets substantially better.** SCP presence was going to force
   nearly every finding in an Organization down to `Likely`. It no longer does.
   `Verified` becomes commonly reachable, which is the difference between a tool
   that answers "yes" and one that answers "probably".
2. **A stated justification for the paid tier partly evaporates.**
   GENERAL_DIRECTION §3 names "resolving the Likely confidence state by checking
   AWS Organizations SCPs and resource-based policies" as what legitimately
   justifies payment. Half of that is now free, done by AWS, server-side. This
   does not break the business model — see §0.3 — but it must not go unnoticed.
3. **ARCHITECTURE.md open question 6 is closed.** No `organizations:DescribeOrganization`
   probe, no `--no-organization` user assertion, no trust hole. Delete the idea.

Residual SCP caveats, which are real but narrow:

- **RCPs (resource control policies) are not supported.** A newer Organizations
  feature, and a genuine remaining blind spot.
- **SCP evaluation does not surface matched statements or missing context
  values**, deliberately, for security. So an SCP-driven denial tells you
  *that* you were denied but not *which* SCP did it — and an SCP condition whose
  value we failed to supply produces a silent denial we cannot explain. This is
  a false-positive source with no diagnostic; see §7.3.

### 0.2 Resource-based policy simulation does not work for roles

The same investigation produced a harder constraint pointing the other way. From
the API reference, stated twice:

> Simulation of resource-based policies isn't supported for IAM roles.

and

> You can also optionally include one resource-based policy to be evaluated with
> each of the resources included in the simulation **for IAM users only**.

Plus:

> The simulation does not automatically retrieve policies for the specified
> resources. If you want to include a resource policy in the simulation, then
> you must include the policy as a string in the `ResourcePolicy` parameter.

CI deploys use roles essentially always. So for our exact use case,
resource-based policy evaluation is **unavailable at any price through this
API** — we cannot pass one, and even for users we would have to fetch it
ourselves first, one policy per call.

`resource_policy_not_evaluated` is therefore a **permanent** `Likely` reason for
resource-policy-capable types, not a temporary gap. This is not a defect in our
design; it is the API's boundary.

### 0.3 What this means for the business model

The paid-tier justification narrows but does not disappear — it relocates, and
arguably to firmer ground:

| Was going to justify payment | Status now |
|---|---|
| SCP evaluation | **Gone.** AWS does it free, server-side. |
| Resource-based policy analysis | **Stronger.** Requires fetching bucket/key/queue policies across accounts — genuinely needs trust and access a CI role should not hold, and must be evaluated *outside* the simulator since the API refuses it for roles. |
| RCP analysis | **New.** Same shape as the above: unsupported by the simulator, needs Organizations visibility. |
| Cross-repo aggregation, trends, org policy templates | Unchanged, and untouched by any of this. |

Note that resource-policy analysis now requires us to build an IAM evaluation
engine rather than delegate to AWS's. That is more work — and a better moat.

**This warrants an edit to GENERAL_DIRECTION §3.** Not a strategy change; a
correction of a factual premise it rests on. Flagged, not made — that document
is the founders' to change.

### 0.4 One capability we did not know we had

`SimulatePrincipalPolicy` returns **`MissingContextValues`** per evaluation
result, and `GetContextKeysForPrincipalPolicy` returns the context keys a
principal's policies actually reference.

Together these turn condition-key handling from "model every condition key in
the mapping database, correctly, forever" into "ask AWS what it needs, supply
what we can, and report precisely what we could not." That is a far smaller and
far more honest problem. See §7.

---

## 1. Scope

**Building:** a single-binary CLI that reads `terraform show -json` output,
resolves the deploying principal, maps each planned change to required IAM
actions, simulates them read-only, and reports per-change confidence.

**Not building (v1):** general IaC security scanning, cross-repo orchestration,
multi-cloud, multi-IaC, full 1,400-type coverage, any hosted component.

**Resource scope (v1):** S3, IAM, EC2, VPC/networking, RDS, Lambda, ECS —
highest-frequency types only, per SPECS §6.

---

## 2. Architecture

```
terraform show -json
        │
        ▼
  internal/plan ──────────► parse; keep managed AWS changes that write
        │
        ▼
internal/principal ───────► sts:GetCallerIdentity → simulatable role ARN
        │                   (+ iam:GetRole when the path is needed)
        ▼
 internal/mapping ────────► resource type → actions;  attributes → ARN
        │
        ▼
 internal/simulate ───────► batch → iam:SimulatePrincipalPolicy → paginate
        │
        ▼
  internal/engine ────────► classify: Verified / Likely / Unchecked
        │
        ▼
  internal/report ────────► text │ json │ sarif
```

**The one structural rule: `internal/engine` performs no I/O.** Everything that
touches the network arrives through an interface. The classification logic is
the part that must never be wrong, so it must be testable without credentials,
network, or an AWS account. This is already true in the scaffold and must stay
true.

---

## 3. The confidence model, precisely

SPECS §5 gives three states. This is the exact decision procedure.

### 3.1 Decision table

Evaluated per (resource change × operation). Read top to bottom; first match wins.

| # | Condition | Level | Reason code |
|---|---|---|---|
| 1 | Resource type absent from mapping database | `Unchecked` | `resource_type_not_mapped` |
| 2 | Type mapped but this operation is not | `Unchecked` | `operation_not_mapped` |
| 3 | Simulation call failed or was not attempted | `Unchecked` | `simulation_failed` |
| 4 | Any of the caveats in §3.2 apply | `Likely` | one or more, all listed |
| 5 | Otherwise | `Verified` | — |

Denial is **orthogonal to confidence**, not a fourth state. A finding carries
both a level and a decision, so "we verified that you cannot do this" and "we
could not fully check whether you can" stay distinguishable. Collapsing them
would recreate the binary pass/fail the spec forbids.

### 3.2 Caveats that force `Likely`

| Reason code | Set when | Permanent? |
|---|---|---|
| `resource_policy_not_evaluated` | mapping marks the type `resource_policy_capable` | **Yes** — API refuses for roles (§0.2) |
| `rcp_not_evaluated` | account is in an Organization *and* the type is resource-policy-capable | **Yes** — simulator does not support RCPs |
| `arn_not_resolvable` | ARN degraded to `*` (§6) | No — improves with better synthesis |
| `arn_partially_resolved` | ARN synthesised from a prefix (§6) | No |
| `condition_keys_not_supplied` | response `MissingContextValues` non-empty | No — improves with mapping coverage |
| `principal_path_unconfirmed` | role path unknown and `iam:GetRole` unavailable (§5) | No |
| `mapping_unverified` | any contributing mapping entry is `status: draft` | No — retires as entries are verified |

`mapping_unverified` is new and deserves a note. Every mapping entry currently
ships as `draft`, meaning nobody has checked it against AWS's reference. A
`Verified` finding built on an unverified action list is a false "safe" one
level down — exactly the failure GENERAL_DIRECTION §6.1 prohibits. So draft
mappings cap the result at `Likely`, and verifying mappings visibly upgrades
real users' results. That turns mapping verification from chores into a feature.

### 3.3 The invariant

> No code path may produce `Verified` by default, by fallthrough, or on error.
> `Verified` is only ever reached by explicitly clearing every caveat.

Written as a test, not a convention: `TestNoDefaultVerified` asserts that a
finding constructed from zero values is `Unchecked`.

---

## 4. Plan ingestion

Already implemented. Decisions worth recording:

- **Decode only the fields we use.** The plan format is large and mostly
  irrelevant; partial decoding makes us tolerant of unrelated format churn.
- **Reject non-plan JSON loudly.** Users pipe state files and `terraform plan`
  text output by mistake. An empty, falsely-clean report is the worst outcome,
  so a missing `format_version` is a hard error.
- **Replacements produce two findings, not one.** `["delete","create"]` needs
  both permissions; a plan that can delete but not recreate fails halfway and
  leaves state torn. Deletes read `before`, creates and updates read `after`.
- **Unknown format major version warns, never fails.** A Terraform upgrade
  should degrade to a visible caveat, not an outage.

**Open:** `terraform show -json` on a *saved plan file* includes
`resource_changes`; run against a state file it does not. Detect and message
this specifically — it will be the single most common user error.

---

## 5. Principal resolution

`sts:GetCallerIdentity` in a GitHub Actions job returns an assumed-role session
ARN. `SimulatePrincipalPolicy` explicitly rejects it:

> You cannot specify the ARN of an assumed role, federated user, or a service
> principal.

So we translate `arn:aws:sts::123:assumed-role/deploy-role/session` →
`arn:aws:iam::123:role/deploy-role`. Implemented and tested.

### 5.1 The path problem — RESOLVED, it is not a problem

The session ARN omits the role's IAM path, so a role at `/platform/deploy-role`
appears only as `assumed-role/deploy-role/session`. This looked like it would
break simulation, and the original design budgeted an `iam:GetRole` call to
repair it.

**Measured 2026-08-31 (E8b, E8c, E8d): it does not break.**

| Experiment | Setup | Result |
|---|---|---|
| E8b | role at `/preflight-contract-fixture/`, simulate via its full ARN | `allowed` |
| E8c | same role, simulate via the **pathless** ARN | `allowed` |
| E8d | a genuinely nonexistent role | `NoSuchEntity` error |

`SimulatePrincipalPolicy` resolves a pathless ARN to the right role. IAM requires
role names to be unique account-wide, so the name alone is an unambiguous key
and the path is redundant for lookup. E8d confirms there is no silent-failure
mode either: a principal that really does not exist errors loudly rather than
returning a misleading denial.

**Consequences:** no `iam:GetRole` call, no `principal_path_unconfirmed` caveat,
one fewer required permission. All three have been removed from the code. This
also retires a caveat that was downgrading *every* assumed-role finding — which
is every CI deploy.

**Required permissions, final:**

| Action | Necessity |
|---|---|
| `iam:SimulatePrincipalPolicy` | required |
| `sts:GetCallerIdentity` | required (usually already allowed) |
| `iam:GetContextKeysForPrincipalPolicy` | **required** — see §7.3; without it we cannot detect the silent-allow trap |

Three actions, all read-only, no Organizations access. Short enough for the
README's first screen, which matters more than it sounds: this is the first
thing a prospective user has to get their platform team to approve.

---

## 6. ARN synthesis — the central accuracy problem

To scope a simulation you need `ResourceArns`. On a create the resource does not
exist, so its identifying attributes are frequently
`(known after apply)`.

### 6.1 Three precision levels

| Level | Produced when | ARN passed | Confidence effect |
|---|---|---|---|
| **Exact** | every template variable resolved from plan attributes | the real ARN | none |
| **Prefix** | name unknown but a `*_prefix` attribute is known | `…:acme-logs-*` *(pending §6.3)* | `arn_partially_resolved` |
| **Wildcard** | nothing derivable | `*` | `arn_not_resolvable` |

### 6.2 Algorithm

```
for each ${Var} in arn_format:
  Partition | Account | Region      → run context
  otherwise:
    attr := arn_attributes[Var]
    if after_unknown[attr] is true:
      if arn_prefix_attributes[Var] known and set → PREFIX
      else                                        → UNKNOWN
    else if after[attr] is a non-empty string     → EXACT
    else                                          → UNKNOWN

all EXACT              → exact ARN
any PREFIX, no UNKNOWN → prefix ARN
any UNKNOWN            → "*"
```

Deletes always read `before`, where names are known — so deletes should almost
always reach Exact. This is a pleasant asymmetry: the destructive operations are
the ones we can check most precisely.

### 6.3 RESOLVED — measured 2026-08-31

Answered empirically against a dedicated scratch account using the fixtures in
`testfixtures/aws`. Raw results are in that directory's README.

| Experiment | Setup | Result |
|---|---|---|
| E1 | policy scoped to one exact ARN, simulate with `ResourceArns: ["*"]` | `implicitDeny` |
| E2 | same policy, simulate with the exact ARN | `allowed` |
| E3 | same policy, simulate with `arn:aws:s3:::preflight-contract-*` | `implicitDeny` |
| E4 | policy scoped to a prefix, simulate with a concrete ARN under it | `allowed` |
| E5 | policy scoped to a prefix, simulate with `["*"]` | `implicitDeny` |

**Q1 — does `["*"]` match ARN-scoped policies? No.** This is the safe answer.
A wildcard simulation of a scoped policy comes back denied, so the failure mode
is a false *positive*, not a false negative.

**Q2 — pattern or literal? Literal.** E3 settles it: if `ResourceArns` were
pattern-matched, `preflight-contract-*` would have covered
`preflight-contract-exact-bucket` and returned allowed. It did not.

**Consequences.**

*The prefix strategy in §6.1 is dead as written.* Passing `…:acme-logs-*` means
"a bucket literally named `acme-logs-*`". Replace it with a **representative
concrete ARN** — `arn:aws:s3:::acme-logs-preflightprobe` — which a
prefix-scoped policy correctly matches and an exact-scoped policy correctly
does not.

*Wildcard results are one-directional, and the two directions must be
classified differently:*

| Wildcard result | Meaning | Level |
|---|---|---|
| `allowed` | the policy grants broadly enough to cover any name the resource ends up with | `Likely` |
| denied | the policy is ARN-scoped; says **nothing** about whether the real ARN is in scope | `Unchecked` |

Reporting a wildcard denial as a missing permission would be a false positive —
the failure mode §7.1 identifies as the one that trains teams to bypass the
tool. It is suppressed instead.

Not fully airtight: an `Allow *` combined with an explicit `Deny` on a specific
ARN would produce a wildcard `allowed` that a real ARN would contradict. That is
why the allowed case is `Likely` and not `Verified`.

### 6.4 The number that decides how good this product is

Run the parser over a corpus of real plans and measure **what fraction of
creates reach Exact**. That single number determines whether ARN synthesis is a
footnote or the main event. Measure it in M1, before optimising anything.

---

## 7. Condition keys

### 7.1 Why this is the top false-positive source

If a matched policy statement is gated on a condition key whose value we did not
supply, the action evaluates to denied. A spurious red check on a deploy that
would have succeeded trains the team to bypass the tool — worse than not
existing.

**Asymmetry to hold onto:** a false negative costs one failed apply. A false
positive costs the tool's credibility. When in doubt, degrade confidence rather
than report a denial.

### 7.2 Design

AWS supplies both halves of the solution:

```
once per run:
  keys := iam:GetContextKeysForPrincipalPolicy(principal)
  needed := keys − auto-populated set
```

Auto-populated by the simulator, never supply these:
`aws:PrincipalAccount`, `aws:PrincipalId`, `aws:PrincipalType`, `aws:Type`,
`aws:UserId`, `aws:UserName`, `aws:PrincipalOrgID`,
`aws:PrincipalOrgMasterAccountId`, `aws:PrincipalOrgPaths`.

For each remaining key, source a value from the plan via a new mapping field
(§8.2). `aws:RequestedRegion` comes from the provider config;
`aws:RequestTag/*` from the resource's `tags`; service-specific keys such as
`ec2:InstanceType` from the named attribute.

Then, per result: **if `MissingContextValues` is non-empty, downgrade to
`Likely` with `condition_keys_not_supplied` and name the keys in the report.**

We do not have to model condition keys exhaustively or correctly in advance. We
ask what is needed, supply what we can, and report precisely what we could not —
which is both easier and more honest.

### 7.3 The silent-allow trap — measured, and worse than expected

`MissingContextValues` is **not** a sufficient signal. Measured 2026-08-31:

| Experiment | Policy gated on | Context supplied | Decision | `MissingContextValues` |
|---|---|---|---|---|
| C1 | `aws:RequestTag/Environment = prod` | none | `implicitDeny` | `[aws:RequestTag/Environment]` |
| C2 | same | `prod` | `allowed` | `[]` |
| C3 | same | `dev` | `implicitDeny` | `[]` |
| R1 | `aws:RequestedRegion = us-east-1` | none | **`allowed`** | **`[]`** |
| R2 | `aws:RequestedRegion = eu-west-1` | none | `implicitDeny` | `[aws:RequestedRegion]` |
| R3 | `aws:RequestedRegion = eu-west-1`, call made *to* eu-west-1 | none | `implicitDeny` | `[aws:RequestedRegion]` |
| R4 | `aws:RequestedRegion = eu-west-1` | `eu-west-1` | `allowed` | `[]` |

**`aws:RequestedRegion` is auto-populated with `us-east-1`, always** — R3 rules
out population from the calling endpoint's region.

So R1 is the trap: a policy gated on `us-east-1`, no value supplied, returns
**`allowed` with an empty `MissingContextValues`**. If the real deploy targets
any other region, the apply fails. We would have reported a confident pass for a
deploy that cannot succeed — a silent false negative, the exact failure the
confidence model exists to prevent.

C1 shows the well-behaved case: a key AWS cannot invent is reported missing and
denies. The difference is entirely *which key it is*, and there is no list we can
rely on staying still.

**Design consequences, all mandatory:**

1. **Always supply `aws:RequestedRegion` explicitly** from the plan's provider
   configuration. Never let it default.
2. **`GetContextKeysForPrincipalPolicy` is required, not recommended.** It is the
   only signal that a key is in play at all, and it correctly reported both
   `aws:RequestTag/Environment` and `aws:RequestedRegion` in testing.
3. **Downgrade on any referenced key we cannot supply, regardless of the
   decision.** An `allowed` needs the same scrutiny as a denial, because the
   trap only fires on the allow path.

### 7.4 The SCP blind spot

> For security reasons, SCP evaluation does not return missing context values.

So an SCP condition key we failed to supply produces a denial with **no
diagnostic at all**. We cannot distinguish "the SCP genuinely denies this" from
"we failed to supply a value the SCP needed."

Mitigation, partial: `GetContextKeysForPrincipalPolicy` reflects identity
policies, not SCPs, so it will not reveal these either. The realistic answer is
a documented caveat plus a `--explain` mode that shows exactly what context we
supplied, so a user debugging a surprising denial can see the gap themselves.
Do not pretend this is solved.

---

## 8. Mapping database

### 8.1 What stays

Root-level `mappings/`, one YAML file per service, `draft`/`verified` status,
loaded via `go:embed`, validated in CI. Contributing needs no Go.

### 8.2 Schema v2

v1 maps an operation to a flat action list, which over-reports: `iam:TagRole` is
only needed when tags are set. Over-reporting is a false-positive source, so v2
makes actions conditional and adds context-key sourcing.

```yaml
resources:
  - type: aws_iam_role
    service: iam
    status: draft
    source: https://docs.aws.amazon.com/service-authorization/...

    arn_format: "arn:${Partition}:iam::${Account}:role/${RoleName}"
    arn_attributes:        { RoleName: name }
    arn_prefix_attributes: { RoleName: name_prefix }

    operations:
      create:
        - action: iam:CreateRole
        - action: iam:TagRole
          when: { attribute_set: tags }
      update:
        - action: iam:UpdateRole
          when: { attribute_changed: [description, max_session_duration] }
        - action: iam:UpdateAssumeRolePolicy
          when: { attribute_changed: [assume_role_policy] }
      delete:
        - action: iam:DeleteRole

    context_keys:
      "aws:RequestTag/*": { from: tags }

    # Actions this resource needs on a DIFFERENT resource it references.
    references:
      - action: iam:PassRole
        arn_from: <attribute holding the referenced ARN>
        when: { attribute_set: <that attribute> }
```

Three additions, each closing a named gap:

- **`when`** — conditional actions. `attribute_set` and `attribute_changed`
  only; deliberately not a general expression language, which would become
  unreviewable and defeat the point of contributors being able to check entries
  by eye.
- **`context_keys`** — sources condition-key values from plan attributes (§7).
- **`references`** — cross-resource requirements. `iam:PassRole` is needed by
  the ECS task definition that *hands over* a role, not by the role. This is the
  most commonly missed IAM permission in real Terraform deploys, and v1's
  per-resource schema could not express it at all.

**Migration:** keep the v1 flat-list form parseable as sugar for
`[{action: X}]`. Existing entries need no rewrite, and simple entries stay
simple — which matters for contribution volume.

### 8.3 Verification workflow

Every entry ships `draft` until someone checks it against the AWS Service
Authorization Reference and records the URL in `source`. Per §3.2, draft
mappings cap findings at `Likely`. Publish coverage — count and verified
fraction — in the README, per GENERAL_DIRECTION §3's "publicly tracked" commitment.

---

## 9. Simulation client

### 9.1 The constraint that shapes everything

> Each operation is evaluated for each resource.

The response is the **cartesian product** of `ActionNames × ResourceArns`.
Naively batching everything into one call produces mostly-meaningless
combinations (`s3:CreateBucket` against an IAM role ARN) and burns the response
budget on them.

Documented limits: `MaxItems` defaults to 100, maximum 1000, with
`IsTruncated`/`Marker` pagination. No documented cap on the *size* of the
`ActionNames` or `ResourceArns` arrays — the 3–128 constraint is per string. So
the real limit is response size, which we control.

### 9.1a Two undocumented behaviours, measured 2026-09-01

Both were found by running the client against a real account, and neither is in
the API reference. Both are now pinned by contract tests.

**The response has two levels, and the top one lies for multi-ARN calls.**
Asking about two buckets under a policy that allows exactly one returns:

```
EvaluationResults[0].EvalResourceName  = "arn:aws:s3:::${BucketName}/${KeyName}"
EvaluationResults[0].EvalDecision      = "implicitDeny"
  ResourceSpecificResults[0]           = {exact-bucket, allowed}
  ResourceSpecificResults[1]           = {other-bucket, implicitDeny}
```

The top-level entry is an **aggregate** carrying a **templated** resource name.
Reading its decision reports the allowed bucket as denied — a false positive on
every batched call. The real per-ARN answers are in `ResourceSpecificResults`.
With a single ARN the top level does carry the real ARN and the right decision,
so both shapes must be handled.

This is why `matchARN` refuses to guess when the echoed name does not match one
we sent. An unmatched pair degrades to `Unchecked`, which is visible; a guessed
attribution would have been a confident wrong answer.

**A wildcard cannot share a call with concrete ARNs.**

```
InvalidInput: Invalid resource input list: you cannot include both * and
individual resources in the resource list for a simulation.
```

Any plan creating one resource with a literal name and another of the same type
whose name is unknown until apply hits this — which is most real plans. Batches
are therefore partitioned into wildcard and concrete before chunking.

### 9.2 Batching

**Group by identical action set *and* identical context.** Resources requiring
the same actions under the same conditions share one call, with all their ARNs.

Grouping on the action set alone — as an earlier draft of this section did — is
a correctness bug once condition-key values are sourced per resource. IAM applies
`ContextEntries` per **call**, not per resource, so two resources with different
tag-derived values sharing a call would each be evaluated under the other's
conditions: a silently wrong answer rather than an error.

```
groups := map[(actionSetHash, contextFingerprint)][]resourceARN

for each (change, operation):
    actions := mapping lookup
    groups[hash(sorted(actions)), fingerprint(context)] append arn

for each group (actions, arns, context):
    partition arns into concrete and wildcard          # see §9.1a
    for each partition:
        for each chunk where len(actions) × len(chunk) ≤ 1000:
            SimulatePrincipalPolicy(...)
            follow Marker while IsTruncated
            read ResourceSpecificResults when present  # see §9.1a
```

**Measured:** 40 resources × 2 actions resolves in **one call, one page, 80
evaluations, no throttling, ~290ms**.

The result cache is keyed `(action, arn, contextFingerprint)` — the context is
part of the identity, since the same action on the same ARN can legitimately
decide differently under different condition values. Everything the response
returns is cached, not only the pairs asked about: the surplus combinations are
already paid for.

### 9.3 Throttling

IAM API rate limits are not publicly documented per-action. Do not invent a
number. Implement adaptive concurrency: start at 2 in-flight calls, exponential
backoff with jitter on `Throttling`/`RequestLimitExceeded`, and let the AWS SDK
v2 retryer do the standard work. Measure real limits in M2 and tune.

### 9.4 Error handling

Every failure degrades to `Unchecked` with a reason. None fails the run silently,
and none produces a pass.

| Failure | Behaviour |
|---|---|
| `AccessDenied` on `SimulatePrincipalPolicy` | hard error, exit 2, print the exact 4-action policy to add |
| `NoSuchEntity` | hard error — principal ARN wrong, likely the role path; tell the user to pass `--principal` |
| Throttling after retries | that batch → `Unchecked`, run continues, warning in report |
| Network/timeout | same |
| Partial pagination failure | results received are kept; the remainder → `Unchecked` |

### 9.5 Interface

**Corrected.** An earlier version of this section claimed the per-ARN interface
was already right and that "batching lives behind it". That was false: with a
call shaped `Simulate(actions, oneARN)` invoked per finding, the implementation
has seen exactly one resource when the first call arrives. It cannot group by
action set, dedupe pairs across resources, or size a chunk — all of which need
the whole question set.

The engine therefore collects every question before any is asked:

```go
type Simulator interface {
    Resolve(ctx context.Context, req Request) (Response, error)
}
```

`Response.Outcomes` is index-aligned with `Request.Items`. Index alignment rather
than a lookup keyed on `(action, ARN, context)` keeps fakes to a few lines,
avoids needing a `[]ContextEntry` as a map key, and makes report ordering
deterministic under concurrency for free.

A simulator that returns a mismatched length is a contract violation the engine
refuses to paper over: everything degrades to `Unchecked` rather than being
matched by guesswork.

The engine still performs no I/O — `Resolve` is one interface call, and batching,
caching, pagination and retry all live behind it.

---

## 10. Output

### 10.1 Text

Grouped by confidence so verified and unverified are never visually conflated.
Denials first — they are the actionable part. Every non-`Verified` finding
states its reason. Already implemented.

### 10.2 JSON

Stable, versioned (`schema_version`), documented as a public contract. Teams
building dashboards on it are exactly the users who later want the platform
layer; breaking their integration is expensive twice over.

### 10.3 SARIF, and why it was not free — IMPLEMENTED 2026-09-25

SARIF is only useful to GitHub if each result carries a file and line. **The plan
JSON contains no source locations** — resources are identified by address
(`aws_s3_bucket.logs`), with no position information anywhere in the document.

Emitting location-less SARIF is worse than emitting none: GitHub accepts it and
then fails to place the annotations, so the feature looks broken rather than
absent.

**Built as designed:** `internal/hclsrc` is a separate HCL pass building an
`address → file:line` index. It is the only package that imports
`hashicorp/hcl/v2`, and `internal/report` reaches it through a
primitive-valued `SourceIndex` interface, so the parser cannot reach the code
that decides whether something is allowed.

The complications resolved as follows.

- **`count` / `for_each`.** The index is keyed on the base address, and lookups
  strip instance keys (`hclsrc.BaseAddress`). Every instance maps back to the
  one block. The stripping scans rather than splitting on punctuation, because
  a `for_each` key may itself contain dots and brackets, and because the module
  half of an address can be expanded too:
  `module.env[0].aws_s3_bucket.logs["a.b"]` → `module.env.aws_s3_bucket.logs`.
- **Modules.** Local module calls (`./`, `../`) are followed and the address
  prefix accumulates. Registry and git modules are deliberately *not* followed
  even though `.terraform/modules` would make it possible: that directory is
  not in version control, so the annotation would name a file the pull request
  does not contain. A missing annotation is the honest outcome, and the index
  names those modules so the gap can be explained.
- **Termination.** A module reaching its own directory through a relative path
  is caught by a path stack, with a depth cap behind it.

**The degradation rule, which is the part that matters.** A result whose
address cannot be located is dropped, never emitted without a location. But a
SARIF run with no results reads to GitHub as a clean bill of health, so dropping
silently would be §3.3's false "safe" wearing a different hat. Every drop is
therefore reported three ways: `executionSuccessful: false`, an error-level
`toolExecutionNotifications` entry naming the count and the addresses, and a
stderr warning. The tool never refuses to run over it — the findings are still
correct and still worth producing.

**Level mapping.** `denied → error`, `unchecked → warning`, `likely → note`,
`verified → omitted`. Verified is omitted because SARIF results are problems and
a verified pass is not one; `likely` is a note rather than a warning because at
current mapping coverage almost everything is `Likely`, and a yellow marker on
every line of a configuration is how a team learns to ignore a tool (§3.2's
concern, applied to the output).

`--config-dir` supplies the directory, defaulting to the plan file's own. It is
kept relative on purpose: a SARIF `artifactLocation.uri` has to be relative to
the repository root, and a runner's absolute path is not.

---

## 11. CLI contract

```
preflight check --plan <file|-> [flags]
preflight mappings list [--status draft|verified]
preflight version
```

| Flag | Default | Notes |
|---|---|---|
| `--plan` | required | `-` for stdin |
| `--principal` | auto | override; auto-resolved via STS when omitted |
| `--format` | `text` | `text` \| `json` \| `sarif` |
| `--config-dir` | the plan file's directory | where the `.tf` files are; SARIF source locations (§10.3) |
| `--fail-on` | `denied` | `denied` \| `likely` \| `unchecked` |
| `--region` | from env | for ARN construction |
| `--config` | `.preflight.yaml` | ignore rules, threshold |
| `--explain` | off | show supplied context and matched statements (§7.3) |
| `--no-color` | auto | honours `NO_COLOR` |

**Exit codes are a CI contract — do not change them.** `0` pass, `1` findings
tripped `--fail-on`, `2` usage or runtime error.

`--fail-on denied` is the default because denial is the one state we are certain
about. Teams that want strictness opt in; teams that get a wall of `Unchecked`
on day one do not have a broken pipeline. Adoption over purity, exactly once,
and visibly.

**Config file** (`.preflight.yaml`) for per-repo ignores:

```yaml
fail_on: denied
ignore:
  - resource: aws_s3_bucket.legacy_logs
    reason: managed out-of-band, deploy role intentionally cannot touch it
```

`reason` is **required** — an ignore without a stated reason is how a check
quietly stops meaning anything. Ignored findings still appear in the report,
marked ignored, never silently dropped.

---

## 12. GitHub Action

```yaml
- uses: <org>/preflight-action@v1
  with:
    plan: plan.json
    fail-on: denied
```

Composite action, downloading a pinned released binary by checksum. Not a
Docker action: container startup dominates runtime for a sub-second tool.

Requires no secrets of its own — it uses the AWS credentials already configured
in the job, which is the whole point. The Action must never request credentials
separately.

---

## 13. Testing

- **No test may require AWS credentials.** Everything network-touching goes
  behind an interface and is faked. Already true; keep it true.
- **Golden-file tests** for all three output formats.
- **`TestShippedDatabase`** validates every mapping entry structurally: ARN
  variables have attributes, actions match the declared service prefix, no
  duplicate types.
- **`TestNoDefaultVerified`** enforces §3.3.
- **Contract tests, opt-in** (`-tags awsintegration`), run manually against a
  real scratch account with deliberately restrictive policies. These are the
  only place §6.3's questions can actually be answered, and they must never run
  in normal CI.
- **Corpus fixtures**: real plan JSON from varied repos, scrubbed of account IDs.
  Also the input to the §6.4 measurement.

---

## 14. Privacy

Offline by default; the only network calls are to the AWS APIs the user points
us at. No telemetry, no login, no phone-home, not even version checks.

The plan file may contain infrastructure detail. It is read, held in memory, and
never written anywhere except the report the user asked for. Reports include
resource addresses and ARNs — documented, so nobody pastes one into a public
issue unaware.

Per GENERAL_DIRECTION §6.3, any future data egress is explicit opt-in. This tool
already asks for real AWS credential access; compounding that trust silently
would be disqualifying.

---

## 15. Build order

Each milestone has an exit criterion that is a demonstration, not a checkbox.

| | Milestone | Exit criterion |
|---|---|---|
| **M0** | Scaffold | ✅ Done. Builds, tests pass, CLI runs, confidence model implemented. |
| **M1** | **Empirical spike** | §6.3 answered with evidence from a real account. §6.4 measured across ≥10 real plans. Wildcard handling in code matches what we learned. |
| **M2** | Simulation client | Real `SimulatePrincipalPolicy` with batching, caching, pagination, backoff. Auto principal resolution. A deliberately under-permissioned role produces a correct, specific denial. |
| **M3** | Mapping v2 + coverage | Schema v2 with `when`/`context_keys`/`references`. S3, IAM, EC2, VPC, RDS, Lambda, ECS. ≥50% of entries `verified`. |
| **M4** | SARIF + Action | HCL location mapping ✅ (`internal/hclsrc`, §10.3). Annotations landing on the right line in a real PR still needs the Action wrapper. |
| **M5** | Release | Signed binaries, GoReleaser, marketplace listing, docs. |

**M1 gates everything.** It is a few days of work that could invalidate
assumptions M2–M5 are built on. Do not start M2 first because it feels more like
progress.

**M1 doubles as the interview artifact.** GENERAL_DIRECTION §9 makes talking to
5–10 outside engineers the top priority. A description-only conversation about a
subtle failure mode gets polite agreement, not signal; running the tool against
the interviewee's own plan file gets real reactions. Do M1, then interview with
it, then decide whether M2–M5 are the right build at all.

---

## 16. Open questions

| # | Question | Blocks | Status |
|---|---|---|---|
| 1 | Does `ResourceArns: ["*"]` match ARN-scoped policies? | confidence correctness | **Closed** — no (§6.3) |
| 2 | Are partial wildcards patterns or literals? | prefix strategy | **Closed** — literal (§6.3) |
| 3 | What fraction of creates reach an Exact ARN? | product quality | **Open** — needs the plan corpus |
| 4 | Real IAM simulate throttling limits? | large-plan performance | Open — M2 measurement |
| 5 | Can SCP-driven denials be distinguished from missing SCP context? | false-positive diagnosis | Open — M2; may be unresolvable |
| 6 | Do `count`/`for_each` addresses map cleanly to HCL positions? | SARIF | Open — M4 |
| 7 | Which other context keys are silently auto-populated? | silent-allow trap (§7.3) | **New, open** — only `aws:RequestedRegion` confirmed so far |

**Closed by the M1 spike, 2026-08-31:**

- Organizations detection — the simulator handles SCPs itself (§0.1).
- Resource-based policies for roles — never simulatable (§0.2).
- Wildcard ARN semantics — measured, and now classified one-directionally (§6.3).
- Partial-wildcard semantics — literal, so the prefix strategy was replaced (§6.3).
- Role path resolution — a non-problem; `iam:GetRole` dropped entirely (§5.1).

**Opened by the M1 spike:** question 7. `aws:RequestedRegion` is auto-populated
with a fixed `us-east-1`, producing a silent `allowed` with no missing-value
warning. We do not know how many other keys behave this way, and there is no
documented list. The mitigation in §7.3 (downgrade on any referenced key we
cannot supply) is deliberately key-agnostic for that reason.

**Still blocking M1's completion:** question 3. It needs the plan corpus, which
no amount of AWS access substitutes for.

---

## 17. Decision log

| Decision | Rationale |
|---|---|
| Engine does no I/O | Classification must be testable without credentials |
| Denial orthogonal to confidence | Preserves the distinction the three-state model exists for |
| Draft mappings cap at `Likely` | A `Verified` built on an unchecked mapping is a false safe one level down |
| Wildcard ARN → `Unchecked` until §6.3 resolves | Assume the dangerous interpretation until measured |
| `iam:GetRole` optional, not required | Adoption friction on the first run is the costliest kind |
| `--fail-on denied` default | Denial is the only state we are certain about |
| Ignores require a stated reason | An unexplained ignore is how a check stops meaning anything |
| No SARIF without source locations | Broken annotations are worse than absent ones |
| Group batches by identical action set | Cartesian product makes naive batching wasteful and noisy |
| One dependency (`yaml.v3`) + AWS SDK | Runs in others' CI with live credentials; dependency surface is a feature |
| Schema v2 `when` is not an expression language | Contributors must be able to check entries by eye |

---

## 18. Sources

- [SimulatePrincipalPolicy API reference](https://docs.aws.amazon.com/IAM/latest/APIReference/API_SimulatePrincipalPolicy.html)
- [IAM policy testing with the IAM policy simulator](https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_testing-policies.html)
- [Permissions for using the policy simulator](https://docs.aws.amazon.com/IAM/latest/UserGuide/permissions-required_policy-simulator.html)
- [IAM and AWS STS quotas](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html)
- [AWS Service Authorization Reference](https://docs.aws.amazon.com/service-authorization/latest/reference/reference_policies_actions-resources-contextkeys.html)
