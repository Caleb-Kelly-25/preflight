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
| `arn_prefix_attributes` | map | no | `${Var}` → a `*_prefix` attribute, for names generated at apply time. See below. |
| `references` | list | no | Actions required against a **different** resource's ARN, such as `iam:PassRole`. See below. |
| `resource_policy_capable` | list | no | Operations during which the resource's own policy can deny. See below. |
| `context_keys` | map | no | Sources condition-key values from plan attributes. See below. |
| `verified_operations` | list | no | Operations proven complete when the entry as a whole is not. See below. |
| `source` | string | no | Reference URL. Required whenever verification is claimed. |
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

## `arn_prefix_attributes`

For resources whose name Terraform generates at apply time from a `*_prefix`
attribute:

```yaml
arn_attributes:
  RoleName: name
arn_prefix_attributes:
  RoleName: name_prefix
```

When the real name is known it wins. When it is not, but a prefix is available,
preflight builds a **representative** name from the prefix and marks the result
inexact — so it can never reach `Verified`, and any denial stays inconclusive.

The point is not to guess the name, which is impossible. It is to beat `*`. M1
measured that `ResourceArns` is a literal rather than a pattern, so passing
`myapp-*` would match nothing at all. But a concrete name *does* match a policy
scoped as `role/myapp-*`, which is how such policies are usually written — so
this turns an `Unchecked` into a `Likely` for a very common configuration.

Only useful where the ARN actually contains the generated name. A security
group's ARN uses its group id, not its name, so a `name_prefix` there cannot
help.

## Cross-resource requirements (`references`)

Some actions are authorised against a **different** resource than the one being
changed. `iam:PassRole` is the case that matters: handing a role to a service is
checked against the *role's* ARN, so the permission belongs to whichever
resource does the handing.

```yaml
references:
  - action: iam:PassRole
    arn_from: role                                          # attribute naming the target
    arn_format: "arn:${Partition}:iam::${Account}:role/${Name}"
    operations: [create, update]                            # omit for all operations
    when: { attribute_set: role }
```

| Field | Meaning |
|---|---|
| `arn_from` | attribute holding the referenced resource. Required. |
| `arn_format` | build an ARN when the attribute holds a bare **name** rather than an ARN. `${Name}` is the attribute's value. Omit when the attribute is already an ARN. |
| `operations` | which operations need it. Empty means all. |
| `when` | same conditions as a conditional action. |

`aws_lambda_function.role` is already an ARN, so it needs no `arn_format`;
`aws_iam_instance_profile.role` is a bare role name, so it does.

If the target ARN cannot be resolved the action is still checked, against `*`,
and the finding is caveated — the same rule as the resource's own ARN. A
reference never silently disappears.

**This is the most commonly missed permission in real Terraform deploys, and it
is not guessable from the docs.** AWS's documentation associates `iam:PassRole`
with *launching* an instance, so reading it suggests `aws_iam_instance_profile`
does not need it. Measured on 2026-09-02, `AddRoleToInstanceProfile` fails
without it:

```
AccessDenied ... not authorized to perform: iam:PassRole
on resource: arn:aws:iam::<acct>:role/<target>
```

## Conditional actions (`when`)

An action can be listed as a bare string, or as a mapping with a condition:

```yaml
operations:
  create:
    - iam:CreateRole                      # always required
    - action: iam:TagRole
      when: { attribute_set: tags }       # only when tags are set
  update:
    - action: iam:UpdateAssumeRolePolicy
      when: { attribute_changed: [assume_role_policy] }
```

The bare-string form is sugar for `{action: X}`, so existing entries need no
rewrite and simple entries stay simple.

Two conditions, and deliberately no more:

| Condition | Holds when |
|---|---|
| `attribute_set: <name>` | the attribute has a value in the state the operation acts on |
| `attribute_changed: [<names>]` | any named attribute differs between prior and planned state |

This is not an expression language on purpose. A contributor has to be able to
check an entry by eye and a reviewer has to be able to tell whether it is right;
anything richer defeats the point of an open, inspectable database.

**Where a condition cannot be decided, the action is kept.** An attribute that is
configured but unknown until apply counts as set, because it will have a value —
we just cannot see it yet. Over-reporting produces a visible false positive;
under-reporting produces a silent false pass.

**Narrowing an action with `when` is the one edit that can introduce a false
pass**, so tie it to evidence. Both shipped uses came from measurement:
`iam:TagRole` is authorised by `CreateRole`'s tags parameter with no TagRole call
ever made, and `s3:PutBucketTagging` is a genuine second call because
`CreateBucket` does not accept tags.

## `resource_policy_capable`

A **list of operations** during which the target resource can be denied by a
policy of its own — S3 buckets, KMS keys, SQS queues, Lambda functions, ECR
repositories, Secrets Manager secrets. `SimulatePrincipalPolicy` does not
evaluate those policies for roles, so those operations can never be `Verified`.

```yaml
resource_policy_capable: [update, delete]            # aws_s3_bucket
resource_policy_capable: [create, update, delete]    # aws_s3_bucket_versioning
```

**It is a list, not a bool, because a resource that does not exist yet has no
policy to deny it.** Measured 2026-09-01 against a bucket whose policy denied
the calling role every `s3` action:

| Call | Result |
|---|---|
| `PutBucketVersioning` on that bucket | denied by the resource policy |
| `GetBucketTagging` on that bucket | denied by the resource policy |
| `CreateBucket` for a **new** bucket | allowed |
| read-backs on the new bucket | allowed |

So `aws_s3_bucket` lists `[update, delete]`. But `aws_s3_bucket_versioning`
lists `create` as well, because *its* create writes to a bucket that already
exists. **The Terraform operation and the AWS resource's lifetime are not the
same thing**, and only the entry's author knows which is which: ask whether this
operation brings the target ARN into existence, or acts on something already
there.

Under-claiming here is a correctness bug, not conservatism: it lets a finding
claim `Verified` when a bucket policy could still deny the call. Over-claiming
is safe but makes `Verified` unreachable for no reason.

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
