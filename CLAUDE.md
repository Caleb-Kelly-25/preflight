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
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — how the code fits together and
  the open questions that are still unresolved.

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

Two direct dependency groups, and the list is deliberately closed:

- `gopkg.in/yaml.v3` — the mapping database.
- `github.com/aws/aws-sdk-go-v2/{config,service/iam,service/sts}` and
  `github.com/aws/smithy-go` — added 2026-09-01 for M2. `config` transitively
  pulls roughly fourteen modules (credentials, IMDS, SSO, internal endpoint
  resolution); hand-rolling credential resolution to avoid it would be worse in
  every dimension, so it stays.

Anything beyond those needs fresh justification. This tool runs in other people's
CI with live AWS credentials in scope, so dependency minimalism is a security
posture, not fussiness.

**No test may require AWS credentials.** Anything touching AWS goes behind an
interface and is faked. `engine.Simulator` is the pattern.

## Layout

`cmd/preflight` CLI · `internal/plan` plan JSON · `internal/principal` identity
resolution · `internal/mapping` mapping DB + ARN templating · `internal/engine`
classification · `internal/finding` confidence types · `internal/report` output
· `mappings/` the database itself.

`mappings/` is at the repo root, not under `internal/`, because the content is a
community asset. Do not move it.

## Conventions

- Comments explain *why*, especially where AWS behaves unintuitively. The
  assumed-role-ARN translation in `internal/principal` is the model: the code is
  short, the reason it exists is not obvious.
- Errors are lowercase and wrapped with context: `fmt.Errorf("opening plan file: %w", err)`.
- Exit codes (0 pass, 1 finding, 2 error) are a CI contract. Do not change them.
- New mapping entries start as `status: draft`. Only mark `verified` with a
  `source` URL that was actually checked.

## Current state

The plan parser, principal resolution, mapping database, confidence model, and
text/JSON output work and are tested. **Not yet implemented:**

- the `iam:SimulatePrincipalPolicy` client — `engine.Options.Simulator` is
  always nil, so every real run currently reports `Unchecked`
- automatic identity resolution (`--principal` is required)
- SARIF output — blocked on HCL source-location mapping
- the GitHub Action wrapper

Do not describe the tool as working end-to-end until the simulator lands.

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
