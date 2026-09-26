# CLAUDE.md

Guidance for Claude Code working in this repository.

## What this is

`preflight` — a CLI that checks whether the identity running `terraform apply`
holds the IAM permissions a plan requires, before merge, without touching real
resources.

Two documents hold the reasoning and must be respected:

- [SPECS.md](SPECS.md) — the MVP technical spec.
- [GENERAL_DIRECTION.md](GENERAL_DIRECTION.md) — strategy, business model, and
  standing decision principles. When something is not covered by the spec,
  resolve it against §6 there.
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — how the code fits together.
  `docs/DESIGN.md` holds the reasoning and the open questions.

The engineering documents are tracked and ship with the repository, as of
2026-09-25. They were excluded while this was a private scratchpad, which left a
clone with no schema reference and no contributor guide — fine for one machine,
wrong for a codebase other people and other agents maintain.

**`GENERAL_DIRECTION.md` is the one exception and is deliberately untracked.**
It holds strategy, business model, competitive positioning and exit context.
Documents here cite it; on a clone those citations point at a file that is not
present, which is intended. Do not commit it, and do not copy its contents into
a tracked file to "fix" a dangling reference.

## The rule that overrides everything

**Never let an unverified check present as safe.** A false "safe" destroys the
tool's only reason to exist. If a code path cannot fully verify something, it
must produce `Likely` or `Unchecked` with a stated reason — never a silent pass.

Practically: when adding a code path that could fail, degrade, or skip, the
default branch produces a downgraded confidence level, not a pass. See
`engine.analyzeOne` for the pattern.

The mirror of this: **false positives are also expensive.** A spurious red check
trains teams to bypass the tool. When a change might over-report required
permissions, say so.

## Build and test

```
go build ./...
go test ./...
go vet ./...
gofmt -l .          # must print nothing
```

Go 1.25+. `make contract` runs the opt-in AWS contract tests; `make test` never
does.

## Dependencies

Three direct dependency groups, and the list is deliberately closed:

- `gopkg.in/yaml.v3` — the mapping database.
- `github.com/aws/aws-sdk-go-v2/{config,service/iam,service/sts}` and
  `github.com/aws/smithy-go` — added 2026-09-01 for M2. `config` transitively
  pulls roughly fourteen modules (credentials, IMDS, SSO, internal endpoint
  resolution); hand-rolling credential resolution to avoid it would be worse in
  every dimension, so it stays.
- `github.com/hashicorp/hcl/v2` and `github.com/zclconf/go-cty` — added
  2026-09-25 for SARIF (M4). SARIF is the primary CI output and is only useful
  to GitHub if every result carries a file and a line; the plan JSON carries no
  source positions at all, so the positions can only come from parsing the
  `.tf` files. Hand-rolling an HCL parser to avoid the dependency would be a
  worse outcome in every dimension that matters — it is the config language
  Terraform itself defines, it changes, and a parser that is subtly wrong about
  block boundaries silently puts annotations on the wrong resource. `hcl/v2`
  adds seven modules to the linked binary (itself, `go-cty`,
  `agext/levenshtein`, `apparentlymart/go-textseg` v15 and v17,
  `mitchellh/go-wordwrap`, `golang.org/x/text`); `golang.org/x/{tools,mod,sync}`
  appear in `go.mod` as indirect but are hcl's own test dependencies and are
  not linked.

  **Confined to `internal/hclsrc`.** Nothing in the engine, the simulator or
  the output layer imports it — `internal/report` talks to the index through a
  primitive-valued interface precisely so that the HCL parser cannot reach the
  code that decides whether something is allowed.

Anything beyond those needs fresh justification. This tool runs in other people's
CI with live AWS credentials in scope, so dependency minimalism is a security
posture, not fussiness.

**No test may require AWS credentials.** Anything touching AWS goes behind an
interface and is faked. `engine.Simulator` is the pattern.

## Layout

`cmd/preflight` CLI · `cmd/derive` mapping-derivation tool (maintainer-only,
`awsderive` tag) · `internal/plan` plan JSON · `internal/principal` identity
resolution · `internal/mapping` mapping DB + ARN templating · `internal/engine`
classification · `internal/simulate` the AWS client, batching and throttling ·
`internal/derive` the derivation loop · `internal/finding` confidence types ·
`internal/hclsrc` resource address → `.tf` file and line, for SARIF ·
`internal/awsref` + `cmd/arncheck` verify every `arn_format` against AWS's
machine-readable service reference · `internal/report` output · `mappings/` the
database itself · `derivefixtures/` Terraform fixtures the derivation runs
against.

Three commands, and only the first ships:

| Command | Ships? | Needs AWS? | Notes |
|---|---|---|---|
| `cmd/preflight` | yes | credentials | the product |
| `cmd/arncheck` | no | no credentials, but network | run by CI on every PR |
| `cmd/derive` | no | credentials, CREATES resources | `awsderive` tag, three guards |

`mappings/` is at the repo root, not under `internal/`, because the content is a
community asset. Do not move it.

**`cmd/preflight` must never import `internal/derive`.** The harness creates real
infrastructure and has no business in the binary users run in CI. A CI job
asserts this.

The engine's unit model is one resource change × one operation, but a unit owns
a **list** of action groups, each with its own ARN and request slot. That is what
lets `iam:PassRole` be checked against the role being handed over rather than
against the resource doing the handing. A unit is only as checked as its
least-checked group.

## Conventions

- Comments explain *why*, especially where AWS behaves unintuitively. The
  assumed-role-ARN translation in `internal/principal` is the model: the code is
  short, the reason it exists is not obvious.
- Errors are lowercase and wrapped with context: `fmt.Errorf("opening plan file: %w", err)`.
- Exit codes (0 pass, 1 finding, 2 error) are a CI contract. Do not change them.
- New mapping entries start as `status: draft`. Promoting to `verified` takes
  **two** things, and a `source` URL alone is not enough — that definition was
  too weak and was corrected on 2026-09-01. Both are required:
  1. **Names correct.** Check every action with `accessanalyzer:ValidatePolicy`,
     not the docs page. It is authoritative, and it caught that
     `s3:PutBucketEncryption` does not exist where a web search claimed it did.
  2. **List complete.** Grant a scratch role exactly the mapped actions, run a
     real apply, then remove one action and confirm it fails. Completeness is
     the dimension that produces false "allowed" results, so it cannot be
     established by inspection.
  3. **Evidence filed.** `mappings/evidence/<type>.json`, written by `cmd/derive`
     and checked by `TestVerifiedEntriesHaveEvidence`. Added 2026-09-26, because
     the first two requirements were unenforceable: a claim could be typed with
     nothing behind it, and deleting a proven action from a verified entry failed
     nothing.
- `read_actions` in the mapping schema holds the read-backs Terraform performs
  after every write. The engine unions them into create, update *and* delete —
  they are not per-operation. Omitting them is how `aws_s3_bucket` came to
  under-report by 14 actions.

### Mapping schema features, and which way each one fails

Full reference: `mappings/SCHEMA.md`. The short version, because each of these
has a safe direction and an unsafe one:

- **`verified_operations`** — operations proven complete when the entry as a
  whole is not. Verification is per-operation: proving create says nothing about
  update.
- **`when`** (`attribute_set` / `attribute_changed`) — conditional actions.
  **This is the one edit that can introduce a false pass**, because it *removes*
  actions. Tie every `when` to a measurement. Undecidable conditions keep the
  action.

  Measure a gate by deriving the type **twice**, from a maximal fixture and a
  minimal one; the difference between the two derived sets is the gate.
  `derivefixtures/aws_vpc` and `aws_vpc__minimal` are the worked example, and
  the only pair measured so far — every other gate in the database is still
  reasoning.

  **Never gate on a boolean without checking its default.** `attribute_set`
  reads a `false` boolean as unset, so a gate on an attribute that defaults to
  *true* drops the action for exactly the configuration that needs it.
- **`references`** — actions required against a *different* resource's ARN.
  `iam:PassRole` is the case that matters and the most commonly missed
  permission in real deploys.
- **`resource_policy_capable`** — a **list of operations**, not a bool. A
  resource that does not exist yet has no policy to deny it, so a bucket's own
  `create` is exempt while `aws_s3_bucket_versioning`'s create is not.
- **`arn_prefix_attributes`** — builds a representative ARN from a `name_prefix`
  when the real name is generated at apply time. Never exact, so it caps at
  `Likely`; it beats `*`, which matches no ARN-scoped policy at all.
- **`arn_or_name`** — declares that an attribute may hold a bare name *or* a
  full ARN (`aws_lambda_permission.function_name`, `aws_ecs_service.cluster`).
  Works identically on a resource's `arn_format` and on a `references` entry.
  **Detection is three-valued on purpose**: name → templated, ARN of the
  declared `service` *and* `resource_type` → used as the target, anything else →
  `*` and inexact. A two-valued "is this an ARN?" test fails the dangerous way,
  because everything it cannot parse gets templated — a partial ARN
  (`123456789012:function:f`) nested inside a synthetic one is confidently
  wrong. An ARN for the *wrong service* is rejected rather than passed through:
  it means the premise is broken, and simulating a `lambda` action against an S3
  ARN manufactures a false positive. Both declaration fields are required;
  without `resource_type`, `role/x` and `user/x` are the same value.

Three tagging mechanisms have been measured across three services, and the
permission is required every time for a different reason: S3 needs a separate
`PutBucketTagging` call, while IAM and EC2 authorise tagging as part of the
create with no separate call at all. None of it is inferable from the API shape.

## Current state

Run `go run ./cmd/preflight mappings list` for the live coverage numbers rather
than trusting a count written here; this section goes stale first.

**The tool works end to end against real AWS.** Plan parsing, principal
resolution, the mapping database, the `iam:SimulatePrincipalPolicy` client
(batching, caching, pagination, adaptive backoff), the confidence model, and
text/JSON/SARIF output all work and are tested. Automatic identity resolution is
wired up — `--principal` is optional and overrides it.

SARIF landed 2026-09-25. `internal/hclsrc` parses the configuration to get the
`address → file:line` index GitHub needs, `--config-dir` points at it, and a
result whose address cannot be located is **dropped, never emitted without a
location** — with `executionSuccessful: false`, a `toolExecutionNotifications`
entry and a stderr warning, because a SARIF run with no results reads to GitHub
as a clean bill of health. See the header comment on `report.WriteSARIF`.

**Shipped, with one honest caveat.** v0.1.0 is released (GoReleaser, six
archives, `checksums.txt`), `action.yml` is a composite Action that downloads the
pinned binary and verifies its checksum, and `.github/workflows/release.yml`
builds on tag. An earlier version of this section claimed both were "not yet
implemented", which was wrong and is the kind of staleness to check rather than
trust.

**The caveat, and it is sharper than it looks: the Action cannot work at all
while this repository is private.** Release assets of a private repository are
not downloadable without credentials, so the action's `curl` gets a flat 404.
Measured 2026-09-26 by actually running it on ubuntu, macOS and Windows runners —
all three failed identically, before reaching the binary.

That is **not** a bug in `action.yml`. The URL it builds is correct and the
archive name it asks for is exactly the name published; the `release-assets` CI
job proves the second half on every push. It is a fact about where the artifacts
live, and it resolves itself when the repository goes public, with no code change.

Two consequences worth keeping straight:

- The `action-smoke` CI job is **gated on `github.event.repository.private ==
  false`** and enables itself when that changes. It is dormant, not broken — a
  job that fails for a reason nobody intends to fix is worse than no job.
- The SARIF round trip (annotations landing on the right line in a real pull
  request) is still unconfirmed, and additionally needs AWS credentials in
  repository secrets. That one is an owner decision, not something to automate.

`release-assets` carries the coverage in the meantime: it reconstructs every
archive name the action can ask for and asserts each is published AND listed in
`checksums.txt`. The action builds that name from `RUNNER_OS`/`RUNNER_ARCH` while
goreleaser decides what exists; nothing else connects the two, so dropping a
platform from the build matrix would break the action silently and only for the
users on it.

**The binding constraint is mapping coverage and verification, not code.** Most
entries are `draft`, and a draft operation caps its findings at `Likely` no
matter how well the engine works. Broadening coverage is the highest-value work
available and needs no Go.

## Deriving a mapping (the loop that makes `verified` possible)

`internal/derive` + `cmd/derive` automate the empirical half of verification.
The loop grants a scratch role exactly the mapped actions, applies a real
fixture, and removes actions one at a time to find which are load-bearing.

```
make derive TYPE=aws_vpc FIXTURE=./derivefixtures/aws_vpc
make derive TYPE=aws_iam_policy OPERATION=update   FIXTURE=./derivefixtures/aws_iam_policy__update
```

Guarded three ways, because it creates real billable resources: the `awsderive`
build tag, `PREFLIGHT_DERIVE_ACCOUNT` checked against a **live**
`GetCallerIdentity`, and `PREFLIGHT_DERIVE_CONFIRM=creates-real-resources`.

- **Do not reuse the `awsintegration` tag for it.** The contract tests are
  documented as costing zero with no blast radius, and that must stay true.
- `internal/derive`'s loop is pure and unit-tested with fakes; only the
  `awsderive`-tagged files touch AWS or Terraform.
- Fixtures live in `derivefixtures/<type>/`. A derived set is only valid for the
  provider version that produced it, and **the authoritative record is
  `provider_version` in the evidence file**, which `cmd/derive` reads from the
  lock file Terraform actually wrote rather than taking anyone's word for it.
  Entry `notes` mention versions too, as prose for a human; where the two
  disagree the evidence file is right. (They did disagree: three update runs
  recorded 6.63.0 by hand and had actually run on 6.66.0.)

  No fixture commits a `.terraform.lock.hcl`, although `.gitignore` permits one,
  because the harness stages each fixture into a temp copy and Terraform writes
  the lock there. Reading the version back out of that temp copy is what makes
  the evidence trustworthy without pinning.
- The harness emits a **report**, never an edited YAML. `verified` is a claim a
  person makes, and the PR should say how completeness was established.
- It DOES write `mappings/evidence/<type>.json`. That is not the same thing: the
  YAML holds a claim, the evidence file holds a transcript. See below.
- **No delete path can be derived**, structurally: teardown always runs with
  operator credentials, so a missing delete permission can never surface. Delete
  paths marked verified were established some other way.

### `verified` is machine-checkable, and the check is asymmetric

`mappings/evidence/<type>.json` records what each derivation run measured, and
`TestVerifiedEntriesHaveEvidence` refuses a verification claim with no run behind
it. `cmd/derive` writes these files; nobody writes one by hand.

Before this existed there were **12 verification claims across 11 entries backed
only by prose in a `notes` field**, and one of them had no notes at all. Two
failures were invisible: typing `verified_operations: [create]` with nothing
behind it, and — the dangerous one — **deleting a proven action from a verified
entry**, which turns a proven claim into a false pass with no symptom.

**The rule is `evidence ⊆ entry`, never equality, and this matters.** An entry
legitimately holds more than any single run measured, because a run measures one
fixture's shape. `iam:DeletePolicyVersion` is the case that proves it: minimality
said droppable for a one-version update, and it is genuinely required once a
policy hits AWS's five-version cap — which no fixture applying its change once
can reach. Equality would force that action out and ship the false pass the
measurement appeared to justify. `TestEvidenceAllowsTheEntryToHoldMore` guards
the asymmetry against being "tidied up".

Evidence files are read from disk, not embedded: they are a maintainer and CI
artifact and have no business in the shipped binary.

### Deriving an update: the two-phase fixture

An update acts on something, so the loop needs a "before" state — and that state
must be created with **operator** credentials, or the run measures the create and
the update together with no way to separate them. `Applier.Setup` does this, and
it runs *before* `Grant` on every attempt (`TestSetupRunsBeforeEveryGrant` pins
the ordering; it is the whole correctness argument).

The convention is a `phase` variable: 1 is the before shape, applied as operator;
2 is the change, applied by the scratch role. `cmd/derive` **refuses** a
non-create operation against a fixture that declares no `phase` variable, because
both phases would then apply the same config, the scratch role would perform the
create, and the run would report the create path labelled "update".

**Vary exactly one attribute per fixture.** An update path depends on which
attribute changed, so a fixture changing three produces one set and no way to
attribute any of it — and a branch nobody measured is a false pass. Check the
provider docs for `ForceNew` first: a replace is a create plus a delete wearing
an update's name. `aws_iam_policy` needed three fixtures and is the worked
example.

### Things the harness learned the hard way

Each of these was a real defect that silently corrupted or aborted a run. Do not
undo them without understanding what they cost.

- **The seed includes `references`.** It did not, so any entry needing
  `iam:PassRole` could not be derived — the apply hung instead of failing, and
  the run reported "stalled, no attributable denial".
- **A killed apply kills the whole process tree** (`proc_windows.go`,
  `proc_unix.go`). Terraform's provider plugins hold the state file; killing only
  the parent orphaned one, and the next teardown failed on a locked file.
- **The fixture is staged into a temp copy and state lives outside it.**
  `derivefixtures/` sits under a synced folder here, and the sync client held
  `terraform.tfstate` open — unbreakably, even with a force delete.
- **A stall stops the run.** Ending a stall means killing the apply, so state no
  longer describes reality: Terraform may have created a resource without
  recording it, in which case destroy finds nothing, reports success, and leaves
  it behind. Continuing produced a confident wrong answer about the *next*
  action. Sweep and re-run to finish.
- **A failed teardown stops the run too**, for the same reason, and exits
  non-zero.
- **Fixtures take the account as `TF_VAR_account_id`**, never hardcoded. An
  account number in a tracked file is information disclosure in a repository
  meant to go public, and a hardcoded one makes the fixture unusable by any
  contributor.

### Measuring a `when` gate

Derive the type **twice** — from the maximal fixture and from
`<type>__minimal` — and the difference between the derived sets is the gate. Two
traps: a provider `default_tags` block silently tags a "minimal" fixture, and
`attribute_set` reads a `false` boolean as unset, so never gate on a boolean
without checking its default.

**Any gate naming `tags` must also name `tags_all`.** `default_tags` merge into
the computed `tags_all` and never touch `tags`, so a `default_tags`-only change
left every tags gate evaluating false and dropping the tagging action — a false
pass. Measured 2026-09-26; **51 gates across 11 services had it.** It was one
pattern copied, not one entry's mistake, so the guard is
`TestTagGatesAlsoNameTagsAll` rather than a note. Both `attribute_set` and
`attribute_changed` take a bare name or a list, satisfied by any one of them.

## The correction most likely to be reintroduced

**SCPs ARE evaluated by `iam:SimulatePrincipalPolicy`** — in scope, server-side,
including their condition keys and resource scoping, with no caller permission
beyond the simulate call itself.

The original spec assumed the opposite and downgraded every finding on that
basis, which made `Verified` unreachable in any account belonging to an
Organization. That was corrected on 2026-08-31 across SPECS §5,
GENERAL_DIRECTION §3 and §6.1, the README, and the classifier.

Do not add an SCP-based downgrade, an `organizations:DescribeOrganization`
probe, or a `--no-organization` flag. `TestNoSCPDowngrade` guards this.

What *is* still unevaluated, permanently: **resource-based policies** (the API
refuses them for IAM roles, which is what CI always uses) and **RCPs** (the
simulator has no support). Reasoning and sources: `docs/DESIGN.md` §0.

## Things that are decided, not open for redesign

- Three confidence states, never binary pass/fail (SPECS §5).
- Coverage is free forever; only depth and aggregation are ever paid
  (GENERAL_DIRECTION §3).
- Offline by default; anything leaving the user's environment is explicit
  opt-in (GENERAL_DIRECTION §6.3).
- Apache 2.0 on the core, mapping content open.
