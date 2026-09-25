# M1 contract-test fixtures

These provision the deliberately-shaped IAM policies that the M1 spike needs in
order to answer the questions in [docs/DESIGN.md](../../docs/DESIGN.md) §6.3 —
the ones that decide whether our confidence model is correct.

## What this creates

Four IAM roles and one managed policy. Nothing else. **No S3 buckets, no
compute, no data.** The roles reference bucket ARNs that are never created; they
exist purely to give `iam:SimulatePrincipalPolicy` something with a known,
deliberate shape to evaluate.

**Cost: zero.** IAM roles and policies are free.

**Blast radius: none.** Every role is made effectively unassumable (an external
ID nobody has reason to use), and even if assumed would grant only
`s3:CreateBucket` on buckets that do not exist. `SimulatePrincipalPolicy` ignores
trust policies entirely, so making them unassumable costs the tests nothing.

| Fixture | Shape | Answers |
|---|---|---|
| `exact-arn` | allow on one exact bucket ARN | §6.3 Q1, Q2 |
| `prefix-arn` | allow on an ARN prefix | §6.3 Q1 |
| `condition-key` | allow on `*`, gated on `aws:RequestedRegion` | §7.2 |
| `pathed` | role at a non-default IAM path | §5.1 |

## Apply

```bash
cd testfixtures/aws
terraform init
terraform plan          # read this before applying
terraform apply
terraform output -json > fixtures.json
```

Then attach the generated runner policy to whichever identity will run the
tests:

```bash
aws iam attach-role-policy \
  --role-name <your-test-runner-role> \
  --policy-arn "$(terraform output -raw test_runner_policy_arn)"
```

That policy grants four read-only actions, **scoped to these four fixture roles
only**. That scoping is deliberate: `SimulatePrincipalPolicy` discloses the
permissions of whatever principal it is pointed at, so granting it account-wide
would hand the holder a way to enumerate everyone's access in the account.

## Tear down

```bash
terraform destroy
```

Nothing persists and nothing else references these roles.

## The experiments

This is the point of the whole exercise. Each row is one
`SimulatePrincipalPolicy` call; the interesting column is what each outcome
would mean.

| # | Fixture | `ResourceArns` | Context | If `allowed` | If `implicitDeny` |
|---|---|---|---|---|---|
| **E1** | exact-arn | `["*"]` | — | **`*` matches ARN-scoped policies.** Wildcard fallback produces false negatives. Our strict `Unchecked` treatment becomes permanent. | `*` is conservative. Wildcard produces false positives only, and could be relaxed to `Likely`. |
| **E2** | exact-arn | the exact allowed ARN | — | Baseline. Harness works. | Harness is broken — fix before trusting anything else. |
| **E3** | exact-arn | `["arn:…:preflight-contract-*"]` | — | `ResourceArns` has **pattern** semantics. The prefix strategy in §6.1 works as written. | `ResourceArns` is a **literal** resource name. Prefix strategy must be replaced by a representative concrete ARN. |
| **E4** | prefix-arn | a concrete ARN under the prefix | — | Baseline. Policy wildcards match concrete resources as expected. | Something is badly wrong with our understanding. |
| **E5** | prefix-arn | `["*"]` | — | Confirms E1 against the common real-world policy shape. | Confirms E1's safer reading. |
| **E6** | condition-key | `["*"]` | none | The condition was ignored — bad, and would mean silent false negatives. | **Expected.** Check `MissingContextValues` names `aws:RequestedRegion`; that is what §7.2's design depends on. |
| **E7** | condition-key | `["*"]` | `aws:RequestedRegion` supplied | **Expected.** Confirms supplying context from plan attributes works. | Our context-entry encoding is wrong. |
| **E8** | pathed (naive ARN) | any | — | Path is not needed after all — simplifies §5.1. | **Expected** (`NoSuchEntity`). Confirms `iam:GetRole` is genuinely required for pathed roles. |

**E1 is the one that matters most.** If `ResourceArns: ["*"]` returns `allowed`
against a policy scoped to a specific ARN, then every create whose name is
unknown until apply is unverifiable, permanently, and the honest product is
narrower than the spec assumes. Better to learn that in week one than after
building on the opposite assumption.

## If the account is in an AWS Organization

Two extra observations are worth making, and cost nothing:

- Simulate any action an SCP denies, and confirm the simulator returns a denial.
  This validates the §0.1 finding first-hand rather than on documentation alone.
- Check whether that denial carries any `MatchedStatements`. The docs say it
  will not, which is the diagnostic blind spot recorded in §7.3.

If the account is standalone, skip both and note it — the results will simply
say nothing about SCP behaviour.

## Results — run 2026-08-31, a dedicated scratch account

Applied and executed. Account was not in an Organization, so the SCP
observations below were skipped.

| # | Result | Verdict |
|---|---|---|
| E1 | `implicitDeny` | **`*` does not match ARN-scoped policies.** The safe answer — wildcards yield false positives, not false negatives. |
| E2 | `allowed` | baseline sound |
| E3 | `implicitDeny` | **`ResourceArns` is literal, not a pattern.** Prefix strategy replaced by a representative concrete ARN. |
| E3b | `implicitDeny` | unrelated ARN correctly denied |
| E4 | `allowed` | policy wildcards match concrete resources as expected |
| E5 | `implicitDeny` | confirms E1 against the common prefix-scoped shape |
| E6/R1 | **`allowed`, no missing values** | **The trap.** `aws:RequestedRegion` is auto-populated with a fixed `us-east-1`. R3 rules out endpoint-region population. |
| E7/R4 | `allowed` | explicitly supplied context evaluates correctly |
| E7b/C3 | `implicitDeny` | a wrong supplied value correctly denies |
| C1 | `implicitDeny` + key named | a key AWS cannot invent *is* reported missing — so the behaviour differs per key |
| E8c | `allowed` via **pathless** ARN | role path is irrelevant; names are account-unique |
| E8d | `NoSuchEntity` | a nonexistent principal fails loudly, not silently |

Everything above is written up in [docs/DESIGN.md](../../docs/DESIGN.md) §5.1,
§6.3 and §7.3, and the classifier has been changed to match.

Two experiments used `simulate-custom-policy` rather than the fixtures (C1–C3,
R1–R4), since inline policies needed no new roles.

**Note for reruns:** PowerShell mangles inline JSON passed to the AWS CLI. Use
the Bash tool with single quotes, or `file://` with a BOM-free file.

## Recording results

Write outcomes into `docs/DESIGN.md` §6.3 and §16 as they land, and change the
code to match. The point of M1 is not to produce a document; it is to replace
assumptions in the classifier with measurements.
