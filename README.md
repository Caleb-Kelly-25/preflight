# preflight

Catch IAM permission gaps between a Terraform plan and the identity that will
apply it — before merge, without touching a single real resource.

> **Status: works end to end, not yet released.** Checks run against real AWS
> and produce real findings. The mapping database is still small and only one
> entry of ten is `verified`, so coverage is narrow and most results cap at
> `Likely`. SARIF output and the GitHub Action are not built yet. See
> [Roadmap](#roadmap).

## The problem

`terraform plan` succeeding tells you the configuration is internally valid. It
tells you nothing about whether the identity running `apply` can actually
execute it. Permission failures surface after merge, mid-deploy, one error at a
time, through fix-and-rerun cycles.

This has been an open gap in the Terraform AWS provider since 2017.

## How it works

```
terraform plan -out=tf.plan
terraform show -json tf.plan > plan.json

preflight check --plan plan.json
```

preflight maps each planned resource change to the IAM actions AWS requires,
then evaluates those actions against the deploying principal using
`iam:SimulatePrincipalPolicy` — a read-only API. Nothing is created, modified,
or destroyed.

## The confidence model

This is the part that matters. preflight does not emit pass/fail, because a
false "safe" is more damaging than a visible "not checked".

| State | Meaning |
|---|---|
| **Verified** | Simulated against the identity policy with nothing left unevaluated. |
| **Likely** | The identity policy and any in-scope SCPs allow it, but something that could still deny at apply time was not checked — a resource-based policy, an RCP, an ARN unknown until apply, or an unverified mapping entry. Always says which. |
| **Unchecked** | Not checked at all, usually an unmapped resource type. Never treat as passing. |

Every non-`Verified` finding states its reason. `Likely` with no explanation is
the kind of hedge that trains people to ignore the tool.

## Install

Requires Go 1.25 or newer until binary releases exist.

```
go install github.com/Caleb-Kelly-25/preflight/cmd/preflight@latest
```

Or from a clone:

```
go build -o preflight ./cmd/preflight
```

## Usage

```
preflight check --plan <plan.json> [flags]
preflight mappings list
preflight version
```

### `check` flags

| Flag | Default | Meaning |
|---|---|---|
| `--plan` | *(required)* | `terraform show -json` output, or `-` for stdin |
| `--principal` | *(current identity)* | caller ARN to check; see below |
| `--format` | `text` | `text`, `json`, or `sarif` |
| `--fail-on` | `denied` | `denied`, `likely`, or `unchecked` |
| `--region` | *(plan, then environment)* | used to build resource ARNs |
| `--context` | | supply a condition key, repeatable: `--context aws:SourceIp@ip=10.0.0.1` |
| `--explain` | `false` | show the context supplied and the raw decision per action |
| `--timeout` | `5m` | overall budget for AWS calls |

`--context` takes an optional type after `@`: `string` (the default),
`stringList`, `numeric`, `boolean`, `date`, `ip`, or `arn`. The type is not
cosmetic — AWS rejects a context value whose type does not match how the policy
uses the key, so an IP-valued key sent as a string is simply refused.

The principal is resolved automatically via `sts:GetCallerIdentity`, so the
common case needs no flag at all. Pass `--principal` when the identity that will
run `apply` is not the one running preflight — a CI role you assume later, for
instance:

```
preflight check --plan plan.json --principal arn:aws:iam::123456789012:role/deploy
```

### Exit codes

| Code | Meaning |
|---|---|
| 0 | nothing above the `--fail-on` threshold |
| 1 | findings tripped the threshold |
| 2 | usage error or the tool could not run |

### Required AWS permissions

Three actions, all read-only. preflight uses the credentials already configured
in your shell or CI job — it never asks for credentials of its own.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "PreflightSimulate",
      "Effect": "Allow",
      "Action": [
        "iam:SimulatePrincipalPolicy",
        "iam:GetContextKeysForPrincipalPolicy"
      ],
      "Resource": "arn:aws:iam::<account>:role/<your-deploy-role>"
    },
    {
      "Sid": "PreflightIdentity",
      "Effect": "Allow",
      "Action": "sts:GetCallerIdentity",
      "Resource": "*"
    }
  ]
}
```

**Scope the IAM actions to the role being checked, not `*`.**
`SimulatePrincipalPolicy` discloses the permissions of whatever principal it is
pointed at, so an account-wide grant hands the holder a way to enumerate every
identity's access. Run without the permissions and preflight prints this policy
with your ARN already filled in.

`iam:GetContextKeysForPrincipalPolicy` is required, not optional. It reports
which condition keys your policies reference, and that is the only reliable
signal that one is in play — an unsupplied condition key can otherwise produce a
confident "allowed" with no warning at all.

Most CI roles do not have these today.

## Coverage

`preflight mappings list` prints what is currently mapped. **One entry of ten is
`verified`; the other nine are `draft`** — written from working knowledge and not
proven complete. A `draft` entry caps every finding it produces at `Likely`. Do
not rely on those action lists.

Verification takes two things, because a correct-looking list can still be
incomplete, and it is incompleteness that produces a false "allowed":

1. Every action name checked against AWS's own policy validator.
2. Empirical proof the list is *sufficient* — grant a role exactly the mapped
   actions, run a real apply, then remove one action and confirm it fails.

That second step is why only one entry is verified. When `aws_s3_bucket` went
through it, the mapping turned out to be **wrong by 14 actions**: a bucket
create needs 17, not the 3 originally listed, because Terraform reads every
resource back after writing it. Assume the nine remaining entries are wrong in
the same direction until measured.

Coverage is free and always will be. The project never gates *whether*
something gets checked behind payment — only, eventually, how deeply.

## Privacy

Fully offline. No login, no account, no telemetry, no network calls except to
the AWS APIs you point it at. Nothing leaves your CI environment.

## Roadmap

Done: the `iam:SimulatePrincipalPolicy` client with batching, caching,
pagination and adaptive backoff; automatic principal resolution; condition-key
sourcing and the silent-allow mitigation; contract tests against real AWS.

Near-term, in order:

1. **Verify the mapping entries.** Nine of ten are `draft`, which caps their
   findings at `Likely`. This is the highest-value work available and needs no Go.
2. Measure ARN derivability across real plans — what fraction of creates yield an
   exact ARN decides how often `Verified` is reachable at all.
3. Broaden the mapping database: EC2, RDS, Lambda, VPC, ECS.
4. Conditional actions and cross-resource requirements (`iam:PassRole`) in the
   mapping schema.
5. SARIF output, which needs HCL source-location mapping.
6. GitHub Action wrapper and released binaries.

## Contributing

Mapping fixes are the most valuable contribution and need no Go. A fix is only
useful if it comes with the evidence described under [Coverage](#coverage) — an
action list that looks right but has never been run is the failure mode this
project exists to eliminate, and adding one to the database moves the problem
rather than solving it.

## Not to be confused with

- **Checkov / Prisma Cloud** — checks whether an IAM policy is *written* safely.
  Different axis. preflight checks whether the *deploying identity* can execute
  a given plan. Complementary, not competing.
- **Terragrunt / Atmos / HCP Terraform Stacks** — cross-repo orchestration.
  preflight is a verification layer that sits alongside them.

## License

Apache 2.0. See [LICENSE](LICENSE).

The mapping database content under `mappings/` is under the same license and is
intended to stay open and inspectable — verifiable coverage claims are worth
more than the marginal protection of closing it.
