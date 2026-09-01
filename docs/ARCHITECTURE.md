# Architecture

How the pieces fit. **For design decisions and their reasoning, see
[DESIGN.md](DESIGN.md)** — this file is the map, that one is the argument.

## Pipeline

```
terraform show -json          internal/plan       parse; keep managed AWS
        │                                          changes that write
        ▼
   plan.Plan
        │  sts:GetCallerIdentity
        ▼
internal/principal            resolve the session ARN into something
        │                     SimulatePrincipalPolicy will accept
        ▼
   principal.Identity
        │  mappings/*.yaml ──► internal/mapping
        ▼
internal/engine  (collect)    resource type → actions
        │                     plan attributes → resource ARN + context keys
        ▼
   engine.Request             every question, assembled before any is asked
        │
        ▼
internal/simulate             group → batch → paginate → retry → cache
        │
        ▼
   engine.Response            index-aligned with the Request
        │
        ▼
internal/engine  (classify)   confidence level + reasons
        │
        ▼
internal/report               text │ json │ sarif
```

## The two rules that shape everything

**`internal/engine` performs no I/O.** Every AWS call arrives through the
`Simulator` interface, in a single `Resolve`. The classification logic is the
part that must never be wrong, so it is testable with no credentials, no
network, and no AWS account.

**The engine collects every question before asking any of them.** This is what
makes batching possible: a per-resource call cannot group by action set or
dedupe across resources, because the implementation has seen one resource when
the first call arrives. See [DESIGN.md §9.5](DESIGN.md).

## Package layout

| Path | Responsibility |
|---|---|
| `cmd/preflight` | CLI surface, flag parsing, exit codes |
| `internal/plan` | Terraform plan JSON → typed changes |
| `internal/principal` | caller ARN → simulatable principal ARN |
| `internal/mapping` | mapping database, ARN templating, condition-key sourcing |
| `internal/engine` | collect → resolve → classify; the confidence model |
| `internal/finding` | confidence types and the JSON output contract |
| `internal/simulate` | the AWS client: batching, cache, pagination, backoff |
| `internal/report` | output formats |
| `mappings/` | **the mapping database itself** — deliberately not under `internal/` |
| `testfixtures/aws` | Terraform for the contract-test fixtures |

`mappings/` sits at the repo root because the mapping content is a community
asset, not an implementation detail. Fixing an entry should not require reading
any Go. Do not move it.

## Exit codes

Part of the CI contract; do not change these casually.

| Code | Meaning |
|---|---|
| 0 | no findings above the configured threshold |
| 1 | findings tripped the `--fail-on` threshold |
| 2 | usage error, bad input, or the tool could not run |

## Testing

- **No test may require AWS credentials**, except the opt-in contract tests.
  `make test` scrubs `AWS_*` from the environment so a stray real call fails
  loudly rather than quietly succeeding on a developer's laptop.
- `internal/simulate` tests run against fake `iamAPI`/`stsAPI` implementations.
  That seam is the reason the package is testable at all — do not take
  `*iam.Client` directly.
- **Contract tests** (`-tags awsintegration`, plus `PREFLIGHT_CONTRACT_ACCOUNT`)
  run against the fixtures in `testfixtures/aws`. They exist to turn measured
  AWS behaviour into standing regression detectors: each one fails with an
  explanation of what to change if AWS alters the behaviour it pins.
  `make contract` runs them; CI compiles them so they cannot rot.
- `TestNoDefaultVerified` and `TestVerifiedRequiresEveryClearance` enforce the
  rule that nothing reaches `Verified` by default or fallthrough.
- `TestShippedDatabase` validates the mapping files we actually ship.

## Where the open questions live

They were previously listed here and drifted badly — several were resolved by
the M1 spike while this file still described them as unknown. They now live in
[DESIGN.md §16](DESIGN.md), which is kept current alongside the code.
