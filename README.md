# preflight

Catch IAM permission gaps between a Terraform plan and the identity that will
apply it — before merge, without touching a single real resource.

> **Status: works end to end, first release pending.** Checks run against real
> AWS and produce real findings, including SARIF annotations for pull requests.
> The mapping database is still small and most entries are `draft`, so coverage
> is narrow and most results cap at `Likely`. The release pipeline and the
> GitHub Action are built, but no version has been tagged yet — until `v0.1.0`
> exists, `go install` is the only way in. See [Roadmap](#roadmap).

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

No Go toolchain required. Releases are static, CGO-free binaries for
linux, darwin and windows on amd64 and arm64.

**Linux / macOS.** Set `VERSION`, `OS` (`linux` or `darwin`) and `ARCH`
(`amd64` or `arm64`) to match your machine:

```
VERSION=0.1.0 OS=linux ARCH=amd64
BASE="https://github.com/Caleb-Kelly-25/preflight/releases/download/v${VERSION}"

curl -fsSLO "${BASE}/preflight_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "${BASE}/checksums.txt"

# Do not skip this. It is the only thing standing between you and running
# whatever a compromised mirror handed you.
sha256sum --ignore-missing -c checksums.txt     # macOS: shasum -a 256 --ignore-missing -c

tar -xzf "preflight_${VERSION}_${OS}_${ARCH}.tar.gz" preflight
sudo install preflight /usr/local/bin/
```

**Windows (PowerShell).**

```powershell
$Version = '0.1.0'
$Base = "https://github.com/Caleb-Kelly-25/preflight/releases/download/v$Version"
$Archive = "preflight_${Version}_windows_amd64.tar.gz"

Invoke-WebRequest "$Base/$Archive" -OutFile $Archive
Invoke-WebRequest "$Base/checksums.txt" -OutFile checksums.txt

$expected = (Select-String -Path checksums.txt -Pattern ([regex]::Escape($Archive))).Line.Split()[0]
$actual   = (Get-FileHash $Archive -Algorithm SHA256).Hash.ToLower()
if ($expected -ne $actual) { throw "checksum mismatch for $Archive" }

tar -xzf $Archive preflight.exe
```

Releases are not signed yet; the checksum is what you have, so check it.

**In GitHub Actions**, use [the Action](#github-action) rather than any of the
above — it does the download and the checksum verification for you.

**From source**, needing Go 1.25 or newer:

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
| `--config-dir` | *(the plan file's directory)* | where the `.tf` files are; SARIF only |
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

### SARIF output

`--format sarif` produces SARIF 2.1.0 for GitHub code scanning, which renders
each finding as an inline annotation on the pull request.

```
preflight check --plan plan.json --format sarif --config-dir . > preflight.sarif
```

GitHub can only place an annotation that names a file and a line, and the plan
JSON contains neither — it identifies resources by address. preflight therefore
parses the Terraform configuration to find where each resource is declared.
`--config-dir` says where those `.tf` files are, defaulting to the plan file's
directory. Give it a path **relative to the repository root**; an absolute path
from a CI runner is not something GitHub can match to a file in a diff.

Local modules are followed, and `count` / `for_each` instances all map back to
the block that declared them. Registry and git modules are not followed: their
code lives under `.terraform/`, which is not in version control, so GitHub could
not place an annotation there anyway.

A finding whose resource cannot be located is **left out of the report rather
than emitted without a location**, because GitHub rejects an entire SARIF upload
when results carry no location — emitting them would lose the good results too.

Omissions are never silent, because a SARIF run with no results reads as a clean
bill of health. How loudly we can say so depends on the channel, and GitHub's
supported-properties list contains neither `invocation.executionSuccessful` nor
`toolExecutionNotifications`. preflight emits both — they are correct SARIF and
other consumers read them — but **neither reaches a GitHub user**, so neither is
relied on. What does work: a warning on stderr, and, if *nothing* could be
located, a non-zero exit. An empty report from a passing step is the failure
this tool exists to prevent, and the exit code is the only signal CI cannot
overlook. A partial omission still produces a useful report and only warns.

Verified findings produce no annotation. SARIF results are problems, and burying
the real ones under a wall of green is how a check stops being read.

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

## GitHub Action

A composite action — it downloads a pinned release, verifies its SHA-256
against the `checksums.txt` published with it, and runs it. Not a Docker
action: the check itself takes well under a second, so pulling a container
would cost more than the work.

```yaml
name: terraform
on: pull_request

permissions:
  contents: read
  id-token: write         # OIDC, so the job holds no long-lived AWS keys
  security-events: write  # required by upload-sarif

jobs:
  preflight:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      # preflight uses whatever identity the job already has. It configures
      # nothing itself and takes no secrets of its own.
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789012:role/ci-preflight
          aws-region: us-east-1

      - uses: hashicorp/setup-terraform@v3
        with:
          terraform_wrapper: false

      - run: terraform init
      - run: terraform plan -out=tf.plan
      - run: terraform show -json tf.plan > plan.json

      - id: preflight
        uses: Caleb-Kelly-25/preflight@v0.1.0
        with:
          plan: plan.json
          format: sarif
          config-dir: .
          fail-on: denied
          # The role that will run apply, which is not the role running the
          # check. Drop this when they are the same.
          principal: arn:aws:iam::123456789012:role/terraform-deploy

      # always(), because the step above fails when it finds something — and a
      # finding nobody can see on the diff is exactly what the annotations are
      # for.
      - if: always() && steps.preflight.outputs.sarif-file != ''
        uses: github/codeql-action/upload-sarif@v3
        with:
          sarif_file: ${{ steps.preflight.outputs.sarif-file }}
          category: preflight
```

Runs on `ubuntu-*`, `macos-*` and `windows-*` runners, on x64 and arm64.

### Inputs

| Input | Default | Meaning |
|---|---|---|
| `plan` | *(required)* | path to `terraform show -json` output |
| `fail-on` | `denied` | `denied`, `likely`, or `unchecked` |
| `format` | `text` | `text`, `json`, or `sarif` |
| `config-dir` | *(the plan file's directory)* | where the `.tf` files are, relative to the repository root; SARIF only |
| `principal` | *(the job's own identity)* | ARN of the identity to check |
| `region` | *(plan, then environment)* | used to build resource ARNs |
| `version` | *(the action's own version)* | which release to download, e.g. `v0.1.0` |

`version` defaults to the release the action was tagged with, so
`uses: Caleb-Kelly-25/preflight@v0.1.0` runs the `v0.1.0` binary and nothing
floats. Set it only to pin a binary different from the action.

### Outputs

| Output | Meaning |
|---|---|
| `exit-code` | `0`, `1`, or `2` — the [exit code](#exit-codes) preflight returned |
| `sarif-file` | absolute path to the SARIF report, set only when `format: sarif` |

The step fails on exit `1` and `2` — both mean "not ready to merge" — but they
are different failures. `1` is a real finding; `2` is preflight being unable to
answer at all, which is an outage, not a permission gap. Both are named in the
log, and `exit-code` keeps the distinction machine-readable:

```yaml
      - id: preflight
        uses: Caleb-Kelly-25/preflight@v0.1.0
        continue-on-error: true      # needed to read the output after a failure
        with:
          plan: plan.json

      - if: steps.preflight.outputs.exit-code == '2'
        run: echo "preflight could not run — this is not a clean result"
```

Treating `2` as a pass is the one mistake worth guarding against: a check that
could not run is not a check that succeeded.

### What the job's role needs

The three read-only actions listed under
[Required AWS permissions](#required-aws-permissions), granted to the identity
the job assumes — `ci-preflight` in the example above, not the deploy role
being inspected.

## Coverage

`preflight mappings list` prints what is currently mapped, and marks each
operation that has been proven. Most are still `draft` — written from working
knowledge and not proven complete. **A `draft` operation caps every finding it
produces at `Likely`.** Do not rely on those action lists.

Verification takes two things, because a correct-looking list can still be
incomplete, and it is incompleteness that produces a false "allowed":

1. Every action name checked against AWS's own policy validator. (Names only —
   it does **not** check that an action can apply to the ARN it is scoped to.)
2. Empirical proof the list is *sufficient* — grant a role exactly the mapped
   actions, run a real apply, then remove each one and confirm it fails.

That second step is the expensive one, and every entry put through it so far
was wrong:

| Entry | Claimed | Measured |
|---|---|---|
| `aws_s3_bucket` | 3 actions | **17** — Terraform reads each resource back after writing it |
| `aws_iam_role` | missing 5 | including `iam:TagRole`, authorized with no API call ever made |
| `aws_iam_instance_profile` | 5 actions | plus `iam:PassRole` on the *role*, which AWS's docs misdirect you about |
| `aws_vpc` | one surplus | `ec2:DescribeTags` was never needed |
| `aws_security_group` | two surplus | `ec2:DescribeTags` again, plus `DescribeSecurityGroupRules` |

Assume any unmeasured entry is wrong in the same way. Over-reporting is the
safe direction; under-reporting produces the false "allowed" this tool exists to
prevent.

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
5. Tag `v0.1.0`. The [GitHub Action](#github-action) and the GoReleaser pipeline
   that feeds it are written; nothing has been published yet, and until it is,
   the download instructions above describe artifacts that do not exist.
6. Sign the release artifacts. The Action verifies checksums today, which
   protects against a corrupted or swapped archive but not against a compromised
   release process.

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
