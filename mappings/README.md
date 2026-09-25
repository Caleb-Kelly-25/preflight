# The mapping database

This directory maps Terraform resource types to the IAM actions AWS requires to
create, update, and delete them. It is the heart of the tool, and it is open on
purpose: coverage claims are only credible if anyone can check them.

Contributing here needs no Go. Edit a YAML file.

## Status field — read this first

Every entry carries a `status`:

- **`draft`** — not established as correct. May be wrong in either direction:
  missing an action, or requiring one that is not needed.
- **`verified`** — **both** dimensions below have been established.

Run `preflight mappings list` for the current counts. Most entries are `draft`.
Promoting them is the highest-value contribution available, and it needs no Go.

Every entry checked empirically so far was wrong:

| Entry | Claimed | Actually needed |
|---|---|---|
| `aws_s3_bucket` | 3 actions | **17** — Terraform reads each resource back after writing it |
| `aws_iam_role` | missing 5 | including `iam:TagRole`, authorized with no API call ever made |
| `aws_iam_instance_profile` | 5 profile actions | plus `iam:PassRole` on the *role*, which AWS's docs misdirect you about |
| `aws_vpc` | one surplus | `ec2:DescribeTags` was never needed |

Assume any unmeasured entry is wrong in the same way.

Why the distinction is enforced rather than implied: a wrong action list
produces either a false pass (the tool's one unforgivable failure) or a false
block (which trains teams to bypass the tool). Neither is acceptable from a
mapping nobody checked, so the state is visible instead of assumed.

## Verification has two dimensions, and only one of them is easy

This distinction was learned the hard way, and it is why only one entry is
`verified`.

**1. Correctness — are the action names right?**

Easy, and done for every shipped entry. AWS's own policy validator is
authoritative for *names* and beats reading the HTML reference. It does **not**
check that an action can apply to the ARN you scoped it to — see step 1 of
"Promoting an entry" below, which is where that trap is documented:

```bash
aws accessanalyzer validate-policy --policy-type IDENTITY_POLICY \
  --policy-document file://policy.json
```

It reports `INVALID_ACTION` for any action that does not exist, often with the
right name. It caught, for example, that the IAM action is
`s3:PutEncryptionConfiguration` and that `s3:PutBucketEncryption` — the obvious
guess, and the name of the actual API operation — does not exist at all.

**2. Completeness — is anything missing?**

Hard, and done for only a handful of entries. Nothing about a name being valid
says the list is sufficient. Terraform may call actions the resource's
documentation never mentions, and the mechanism differs per service:

- `CreateBucket` does not accept tags, so a tagged bucket almost certainly needs
  a separate `PutBucketTagging` on create.
- `force_destroy` empties a bucket first, needing `s3:ListBucket`,
  `s3:DeleteObject` and `s3:DeleteObjectVersion`.
- Terraform reads every resource back after creating it, and whether the
  provider hard-fails or tolerates `AccessDenied` on those `Get*` calls is
  unresolved.

**Completeness is the dimension that matters most**, because a missing action
produces a false "allowed" for a deploy that will fail — precisely the failure
this tool exists to prevent. Over-reporting is wrong too, but it fails safe.

## Partial verification

Verification is empirical and per-operation. Proving a create succeeds with
exactly the mapped actions says nothing about update, whose required actions
depend on which attributes changed — so most entries become provable one
operation at a time.

`verified_operations` records that:

```yaml
status: draft
verified_operations: [create, delete]
```

Findings for a listed operation can reach `Verified`; every other operation
still caps at `Likely`. Use it rather than holding a whole entry at `draft`
because one operation is unproven, and rather than marking the entry `verified`
when it is not. `aws_iam_role` is the worked example: create and delete were
established against a real apply, update was not.

## Promoting an entry to `verified`

1. **Names.** Run the entry's actions through `validate-policy` as above. Zero
   `ERROR` findings is the bar.

   **This validates names only — not whether an action can apply to the ARN you
   scoped it to.** Measured 2026-09-25: a policy granting `lambda:CreateFunction`
   on `arn:aws:s3:::my-bucket`, and `ecs:CreateService` on a *cluster* ARN,
   produce **zero findings of any severity**, while a planted
   `s3:PutBucketEncryption` in the same document is caught. An earlier version of
   this step claimed filling in `arn_format` validated the ARN. It does not, and
   every `arn_format` in this database is unverified by this procedure.

   To check an `arn_format`, read the **"Resource types" column of the action
   table** in that service's page of the Service Authorization Reference. It
   lists which resource type each action authorizes against.

   Getting it wrong usually *over*-reports — a simulation scoped to the wrong
   ARN is denied, producing a visible false positive. But it can under-report
   when the templated ARN is **broader** than the one AWS actually authorizes
   against, because a policy allowing the broad form then allows our query while
   denying the real call. `aws_lambda_layer_version` has exactly this defect,
   documented in its entry: deletion authorizes against the *versioned* layer
   ARN, and only the unversioned name is knowable at plan time.
2. **Completeness.** Establish it empirically: grant a scratch role exactly the
   mapped actions, run a real `terraform apply` for that resource type, and
   confirm it succeeds. Then remove one action and confirm it fails. Reading the
   provider source (<https://github.com/hashicorp/terraform-provider-aws>) is
   acceptable supporting evidence, but an apply that actually succeeds is the
   only thing that proves the list sufficient.
3. Confirm which Terraform attribute supplies each ARN variable.
4. Set `status: verified`, put the reference URL in `source`.
5. **Say in the PR how you established completeness.** That is the claim being
   made; "read the docs" does not support it.

## Schema

See [SCHEMA.md](SCHEMA.md) for the full field reference.

```yaml
resources:
  - type: aws_s3_bucket                              # Terraform resource type
    service: s3                                      # IAM action prefix
    status: draft                                    # draft | verified
    arn_format: "arn:${Partition}:s3:::${BucketName}"
    arn_attributes:
      BucketName: bucket                             # ${BucketName} ← .bucket
    resource_policy_capable: [update, delete]        # ops a bucket policy can deny
    operations:
      create: [s3:CreateBucket]
      update: [s3:PutBucketTagging]
      delete: [s3:DeleteBucket]
    source: https://docs.aws.amazon.com/...          # required for `verified`
    notes: >-
      Anything a reviewer would otherwise have to rediscover.
```

## Guidelines

**Be precise about ARNs.** `arn_attributes` is what lets preflight scope a
simulation to a specific resource instead of `*`. Every `${Var}` in `arn_format`
needs an entry, and CI enforces that.

**Set `resource_policy_capable` honestly.** It lists the operations a resource's
own policy can deny, forcing those findings down to `Likely` — correct, because
S3 buckets and KMS keys really can be denied by their own policy regardless of
the identity policy. Ask per operation whether it brings the target ARN into
existence (a bucket's own `create` cannot be denied by a policy that does not
exist yet) or acts on something already there (everything else). See
[SCHEMA.md](SCHEMA.md).

**Prefer under-claiming.** Leaving an operation out produces `Unchecked`, which
is visible and safe. Guessing produces a wrong answer that looks confident.

**One file per service**, named for the service: `s3.yaml`, `ec2.yaml`.

## Validation

```
go test ./internal/mapping/...
```

`TestShippedDatabase` checks that every file parses, no resource type is defined
twice, every ARN variable has an attribute, and every action uses its resource's
declared service prefix.
