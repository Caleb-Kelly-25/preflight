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
`internal/report` output · `mappings/` the database itself ·
`derivefixtures/` Terraform fixtures the derivation runs against.

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

**Not yet implemented:**

- the GitHub Action wrapper, and released binaries

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
```

Guarded three ways, because it creates real billable resources: the `awsderive`
build tag, `PREFLIGHT_DERIVE_ACCOUNT` checked against a **live**
`GetCallerIdentity`, and `PREFLIGHT_DERIVE_CONFIRM=creates-real-resources`.

- **Do not reuse the `awsintegration` tag for it.** The contract tests are
  documented as costing zero with no blast radius, and that must stay true.
- `internal/derive`'s loop is pure and unit-tested with fakes; only the
  `awsderive`-tagged files touch AWS or Terraform.
- Fixtures live in `derivefixtures/<type>/`, and their `.terraform.lock.hcl` is
  committed on purpose: a derived set is only valid for the provider version
  that produced it.
- The harness emits a **report**, never an edited YAML. `verified` is a claim a
  person makes, and the PR should say how completeness was established.

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
