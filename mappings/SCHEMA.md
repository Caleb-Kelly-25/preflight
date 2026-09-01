# Mapping schema reference

Each `*.yaml` file in this directory is a document with a single top-level key:

```yaml
resources:
  - <resource entry>
  - <resource entry>
```

## Resource entry fields

| Field | Type | Required | Meaning |
|---|---|---|---|
| `type` | string | yes | Terraform resource type, e.g. `aws_s3_bucket`. Must be unique across all files. |
| `service` | string | yes | IAM action prefix, e.g. `s3`. Every action in `operations` must use it. |
| `status` | `draft` \| `verified` | yes | See [README.md](README.md). |
| `operations` | map | yes | `create` / `update` / `delete` → list of IAM actions. At least one. |
| `arn_format` | string | no | ARN template using `${Var}` placeholders. |
| `arn_attributes` | map | no | `${Var}` → Terraform attribute name. |
| `resource_policy_capable` | bool | no | Set when the resource can carry its own policy. Forces findings to `Likely`. |
| `context_keys` | map | no | Sources condition-key values from plan attributes. See below. |
| `source` | string | no | Reference URL. Required in practice for `verified`. |
| `notes` | string | no | Anything a reviewer would otherwise rediscover. |

## `operations`

Keys are exactly `create`, `update`, and `delete`. Anything else is a load
error.

```yaml
operations:
  create:
    - s3:CreateBucket
  update:
    - s3:PutBucketTagging
  delete:
    - s3:DeleteBucket
```

Actions must be `service:ActionName` and the prefix must match `service`.

An omitted operation is not "no permissions needed" — it produces `Unchecked`
for that operation, which is the safe outcome. Omit deliberately rather than
guessing.

A replacement (destroy-and-recreate) is evaluated as both `delete` and `create`,
so both need to be present for a replacement to be fully checked.

## `arn_format` and `arn_attributes`

`arn_format` is an ARN with `${Var}` placeholders. Three are supplied
automatically from the run context:

| Placeholder | Source |
|---|---|
| `${Partition}` | the caller's ARN partition (`aws`, `aws-us-gov`, `aws-cn`) |
| `${Account}` | the caller's account ID |
| `${Region}` | the `--region` flag |

Every other placeholder must have an `arn_attributes` entry naming the Terraform
attribute that supplies it. This is enforced by `TestShippedDatabase`.

```yaml
arn_format: "arn:${Partition}:iam::${Account}:role/${RoleName}"
arn_attributes:
  RoleName: name
```

On a `delete`, values come from the plan's `before` state; on `create` and
`update`, from `after`.

If any placeholder cannot be filled — the attribute is absent, or Terraform
marked it unknown-until-apply — the ARN degrades to `*` and the finding gains
the `arn_not_resolvable` reason. This is common on creates and is the tool's
central accuracy problem; see open question 1 in
[docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md).

## `resource_policy_capable`

Set this for resource types that can carry a policy of their own — S3 buckets,
KMS keys, SQS queues, Lambda functions, ECR repositories, Secrets Manager
secrets. `SimulatePrincipalPolicy` does not evaluate those policies, so a
finding on such a resource can never be `Verified`.

Under-claiming here is a correctness bug, not a conservatism: it lets a finding
claim `Verified` when a bucket policy could still deny the call.

## `context_keys`

Sources IAM condition-key values from plan attributes.

```yaml
context_keys:
  "aws:RequestTag/*":    { from: tags }
  "s3:x-amz-acl":        { from: acl }
  "s3:max-keys":         { from: max_keys, type: numeric }
```

| Field | Required | Meaning |
|---|---|---|
| `from` | yes | The Terraform attribute to read the value from |
| `type` | no | IAM context value type: `string` (default), `stringList`, `numeric`, `boolean`, `date`, `ip`, `arn` |

A key ending in `/*` matches any key with that prefix, and the tail indexes into
`from`, which must then be a map: `aws:RequestTag/Environment` reads
`tags["Environment"]`. **That single trailing-glob form is the only pattern
supported** — anything richer stops a contributor from being able to check an
entry by eye.

**Why this matters more than it looks.** If a policy's condition is gated on a
key we do not supply, the result is not reliably a denial. Measured 2026-08-31:
`aws:RequestedRegion` left unsupplied comes back **`allowed`** with an empty
`MissingContextValues`, because the simulator substitutes its own value. A key
we cannot source therefore downgrades confidence regardless of the decision, and
declaring a source here is what avoids that downgrade.

`aws:RequestedRegion` is supplied automatically from the plan and needs no
declaration.

A value that cannot be sourced — the attribute is absent, is unknown until
apply, or is not a scalar — is never guessed at. It becomes an
`unsupplied_context_keys` entry and a visible caveat.

## Not yet expressible

Known gaps in the schema, tracked in
[docs/DESIGN.md](../docs/DESIGN.md):

- **Conditional actions.** `iam:TagRole` is only needed when tags are set; the
  schema lists it unconditionally, which over-reports.
- **Attribute-scoped updates.** An update only needs the actions for the
  attributes that actually changed.
- **Cross-resource requirements.** `iam:PassRole` is needed by the resource that
  *hands* a role to a service, not by the role itself.
- **ARN prefix synthesis.** Where a name is generated from a `*_prefix`
  attribute, a representative concrete ARN would be more informative than `*`.

Use `notes` to record these where they apply, so the information is not lost
before the schema catches up.
