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

**Every entry in this repository is currently `draft`.** Promoting entries is
the highest-value contribution available, and it needs no Go.

Why the distinction is enforced rather than implied: a wrong action list
produces either a false pass (the tool's one unforgivable failure) or a false
block (which trains teams to bypass the tool). Neither is acceptable from a
mapping nobody checked, so the state is visible instead of assumed.

## Verification has two dimensions, and only one of them is easy

This distinction was learned the hard way and is the reason nothing is
`verified` yet.

**1. Correctness — are the names and ARNs right?**

Easy, and now done for every shipped entry. AWS's own policy validator is
authoritative and beats reading the HTML reference:

```bash
aws accessanalyzer validate-policy --policy-type IDENTITY_POLICY \
  --policy-document file://policy.json
```

It reports `INVALID_ACTION` for any action that does not exist, often with the
right name. It caught, for example, that the IAM action is
`s3:PutEncryptionConfiguration` and that `s3:PutBucketEncryption` — the obvious
guess, and the name of the actual API operation — does not exist at all.

**2. Completeness — is anything missing?**

Hard, and **not done for any entry**. Nothing about a name being valid says the
list is sufficient. Terraform may call actions the resource's documentation
never mentions:

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

## Promoting an entry to `verified`

1. **Names and ARNs.** Run the entry's actions through `validate-policy` as
   above, with `Resource` set to the entry's `arn_format` filled in. Zero
   findings is the bar.
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
    resource_policy_capable: true                    # can carry its own policy
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

**Set `resource_policy_capable` honestly.** It forces the finding down to
`Likely`, which is correct — S3 buckets and KMS keys really can be denied by
their own policy regardless of the identity policy.

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
