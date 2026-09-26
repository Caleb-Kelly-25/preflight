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
| `arn_or_name` | map | no | `${Var}` → a declaration that the source attribute may hold a full ARN instead of a bare name. See below. |
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

## `arn_or_name`

Several Terraform attributes accept **either a bare name or a full ARN**, and
both spellings are common in real configurations:

| Attribute | Name spelling | ARN spelling |
|---|---|---|
| `aws_lambda_permission.function_name` | `my-function` | `aws_lambda_function.x.arn` |
| `aws_ecs_service.cluster` | `prod` | `aws_ecs_cluster.x.id` |
| `aws_ecs_service.iam_role` | `ecs-service` | `aws_iam_role.x.arn` |

Substitution is unconditional, so an entry that templates such an attribute
nests one ARN inside another for half its users — a string that looks like an
ARN, matches no policy ever written, and is reported with full confidence. Both
Lambda and ECS entries used to omit `arn_format` entirely to avoid that, and
paid for it with `*` on every operation.

`arn_or_name` says the value must be **inspected before it is used**:

```yaml
arn_format: "arn:${Partition}:lambda:${Region}:${Account}:function:${FunctionName}"
arn_attributes:
  FunctionName: function_name
arn_or_name:
  FunctionName: { service: lambda, resource_type: function }
```

It works the same way on a `references` entry, where it takes no variable key
because a reference has only one attribute:

```yaml
references:
  - action: iam:PassRole
    arn_from: iam_role
    arn_format: "arn:${Partition}:iam::${Account}:role/${Name}"
    arn_or_name: { service: iam, resource_type: role }
```

| Field | Required | Meaning |
|---|---|---|
| `service` | yes | the ARN service segment the value must carry, e.g. `lambda` |
| `resource_type` | yes | the resource-type segment, e.g. `function` in `function:my-fn` or `cluster` in `cluster/prod` |

Both are required. Without `service`, any ARN at all is accepted; without
`resource_type`, `arn:…:role/x` and `arn:…:user/x` are the same value, and
building one out of the other is exactly the confident wrong answer the field
exists to remove.

### The three outcomes

Detection is **three-valued, not two-valued**, and that is the whole safety
argument. A plain "is this an ARN?" test fails in the dangerous direction:
everything it cannot parse gets templated.

| The value is | What happens |
|---|---|
| a bare name (no `:` and no `/`) | templated through `arn_format`, exact |
| an ARN of the declared service **and** resource type | used as the target, exact |
| anything else | `*`, `exact=false`, `arn_not_resolvable` |

The third row is what catches the values that are *nearly* an ARN — a partial
Lambda ARN (`123456789012:function:f`, which the Lambda API genuinely accepts),
an alias-qualified name (`my-fn:PROD`), the resource half of an ARN pasted on its
own (`cluster/prod`), a container tag (`myapp:v2`). None of them is a name, so
none of them is templated.

**An ARN for the wrong service is rejected, not passed through.** It is a
well-formed ARN, so this is a deliberate choice: a `lambda` declaration seeing
`arn:aws:s3:::my-bucket` means the premise is broken, and simulating a `lambda`
action against an S3 ARN produces an implicit deny — a false positive
manufactured out of a modelling error. `*` says "not checked", which is true.

### Which direction it fails

**Toward `*`, on every ambiguity.** Every check in the parser — segment
charset, six fields, a twelve-digit or empty or `aws` account, a resource id
with no further separators — rejects into the wildcard, never into a built ARN.
The cost is a lost scoping and therefore a `Likely` where a `Verified` was
possible; the cost of the other direction is a `Verified` against an ARN that
names nothing.

**The one thing to get right is the `service` / `resource_type` pair.** Declare
them for the resource the attribute points at, not for the resource being
changed: `aws_ecs_service.cluster` holds a **cluster** ARN even though the entry
builds a **service** ARN.

### Region and account follow the value

When the attribute holds an ARN, its partition, account and region replace the
caller's for the rest of that template. An ECS service lives in the region and
account of its cluster, not of whoever is running `terraform`, so a cluster ARN
in `eu-west-1` builds a service ARN in `eu-west-1`. Using the caller's would
produce a well-formed ARN naming a resource that does not exist.

On a `references` entry the ARN is used verbatim for the same reason — it is
already the target, and decomposing and rebuilding it would quietly relocate a
cross-account role into the account running the plan.

If two declared attributes in one template disagree about partition, account or
region, neither is used and the ARN degrades to `*`.

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
| `arn_or_name` | the attribute may hold **either** spelling; inspect it first. Requires `arn_format`. See [`arn_or_name`](#arn_or_name). |
| `operations` | which operations need it. Empty means all. |
| `when` | same conditions as a conditional action. |

`aws_lambda_function.role` is already an ARN, so it needs no `arn_format`;
`aws_iam_instance_profile.role` is a bare role name, so it does. Where the
provider documents one spelling but the API accepts both —
`aws_ecs_service.iam_role` — use `arn_or_name` rather than betting on the
documentation.

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
      when: { attribute_set: [tags, tags_all] }   # only when tagged
  update:
    - action: iam:UpdateAssumeRolePolicy
      when: { attribute_changed: [assume_role_policy] }
```

The bare-string form is sugar for `{action: X}`, so existing entries need no
rewrite and simple entries stay simple.

Two conditions, and deliberately no more:

| Condition | Holds when |
|---|---|
| `attribute_set: <name>` or `[<names>]` | **any** named attribute has a value in the state the operation acts on |
| | **A `false` boolean counts as UNSET.** See the warning below before gating on one. |
| `attribute_changed: <name>` or `[<names>]` | **any** named attribute differs between prior and planned state |

Both accept a bare name or a list, and a list is satisfied by **any one** of its
names. Naming several is not a convenience — see the next section for the case
where naming only one is a false pass.

### Always name `tags_all` alongside `tags`

**Any gate mentioning `tags` must mention `tags_all` too.** `TestTagGatesAlsoNameTagsAll`
enforces it, and CI fails otherwise.

Provider-level `default_tags` merge into each resource's *computed* `tags_all`
and never touch its `tags`. So a plan that changes only `default_tags` shows
`tags_all` changed with `tags` untouched, a gate reading `tags` alone evaluates
false, the tagging action is dropped, and the engine reports a plan as safe that
cannot apply.

This was measured on 2026-09-26, not reasoned: a `default_tags`-only change to an
otherwise untouched `aws_iam_policy` requires `iam:TagPolicy`
(`derivefixtures/aws_iam_policy__update_default_tags`). **51 gates across 11
services had the defect** — it was one pattern copied, not one entry's mistake,
which is why the guard is a test rather than a note.

On create, `attribute_set: [tags, tags_all]` is also the correct form: a resource
with no `tags` of its own still ends up tagged if the provider sets defaults.

This is not an expression language on purpose. A contributor has to be able to
check an entry by eye and a reviewer has to be able to tell whether it is right;
anything richer defeats the point of an open, inspectable database.

**Where a condition cannot be decided, the action is kept.** An attribute that is
configured but unknown until apply counts as set, because it will have a value —
we just cannot see it yet. Over-reporting produces a visible false positive;
under-reporting produces a silent false pass.

### Never gate on a boolean without checking its default

`attribute_set` treats a `false` boolean as unset, because an empty string, an
empty map and `false` are all "the user did not ask for anything". That is
usually right and is occasionally a trap.

It is safe when the attribute **defaults to false**, because "unset" and
"explicitly false" then need the same (absent) API call.
`aws_subnet.map_public_ip_on_launch` is that case, and the gate there is correct.

It is **dangerous when the attribute defaults to true**, because the meaningful
non-default is `false` — exactly the value the gate reads as absent. Gating
`ec2:ModifyVpcAttribute` on `aws_vpc.enable_dns_support` would drop the action
for the one configuration that needs it. That action is therefore listed
unconditionally and over-reports on default VPCs, which is the correct side to
err on.

Measured 2026-09-25: an untagged VPC derived to three actions where a tagged one
derived to five, which proved the `tags` gate correct and `ec2:ModifyVpcAttribute`
surplus for defaults at the same time.

### Measuring a gate

A gate is only reasoning until the negative case has been run. Derive the same
resource type from two fixtures — a maximal one with the attribute set, and a
minimal one without — and the difference between the derived sets *is* the gate.
`derivefixtures/aws_vpc` and `derivefixtures/aws_vpc__minimal` are the worked
example.

One trap when writing the minimal variant: a provider `default_tags` block
applies to every resource in the configuration, so a minimal fixture that keeps
one silently tags the resource and measures nothing.

### Measuring an update gate

An update path depends on **which attribute changed**, so each branch of each
gate is a separate measurement and a branch nobody measured is a false pass. Use
a two-phase fixture — see [derivefixtures/README.md](../derivefixtures/README.md)
— and vary exactly one attribute per fixture.

`aws_iam_policy` is the worked example and took three fixtures: a document
change, a tag change, and a `default_tags` change. Before they were run, the
entry listed nothing at all for a tags-only update, so a plan changing only tags
would have been reported as needing no tagging permission.

**Narrowing an action with `when` is the one edit that can introduce a false
pass**, so tie it to evidence. The tagging gates came from measurement:
`iam:TagRole` is authorised by `CreateRole`'s tags parameter with no TagRole call
ever made, and `s3:PutBucketTagging` is a genuine second call because
`CreateBucket` does not accept tags.

### Fixed-target references: an action against a resource the plan cannot name

Omit `arn_from` and give `arn_format` a complete ARN with no `${Name}`:

```yaml
references:
  - action: route53:GetChange
    arn_format: "arn:${Partition}:route53:::change/*"
    operations: [create, update, delete]
```

This is for an action authorised against a resource whose identity is not in the
plan **and never could be**. Route 53 answers every change with an ephemeral
change id that the provider then polls; no attribute holds it, so the only honest
target is the wildcard a real policy actually grants.

**The alternative is not merely imprecise, it is wrong.** `read_actions` are
scoped to the resource's own ARN, so putting `route53:GetChange` there would ask
AWS whether the caller may call it on a *hostedzone* — a question that always
answers implicit-deny, because the action does not apply to that resource type.
A policy correctly granting it on `change/*` would then be reported as a missing
permission on every Route 53 plan.

`${Partition}`, `${Account}` and `${Region}` are still filled from the caller.
`${Name}` is rejected at load time: there is no value to fill it from, and an
unfilled placeholder degrades the whole ARN to `*`, silently turning a scoped
check into an unscoped one.

## `status` and `verified_operations` need evidence

Both are verification claims, and a claim needs a record. `mappings/evidence/<type>.json`
holds it, `cmd/derive` writes it, and `TestVerifiedEntriesHaveEvidence` fails CI
when a claim has no run behind it.

```json
{
  "resource_type": "aws_iam_policy",
  "runs": [
    {
      "operation": "update",
      "fixture": "derivefixtures/aws_iam_policy__update",
      "derived_at": "2026-09-26",
      "provider_version": "6.66.0",
      "sufficiency": "proven",
      "minimality": "proven",
      "attempts": 8,
      "actions": ["iam:CreatePolicyVersion", "iam:ListPolicyVersions",
                  "iam:GetPolicy", "iam:GetPolicyVersion"],
      "note": "Policy DOCUMENT changed."
    }
  ]
}
```

One operation can hold several runs, and for an update it usually must: each
branch of an `attribute_changed` gate is its own measurement. The merge key is
`(operation, fixture)`, so re-running a fixture supersedes its old record rather
than accumulating two.

`provider_version` is read from the lock file Terraform wrote, not supplied by
hand. A derived set is only valid for the provider that produced it.

### The check is `evidence ⊆ entry`, not equality

Every action a run measured must appear in the entry. The entry may hold **more**.

Both halves are load-bearing. The first catches the dangerous edit — deleting a
proven action, which converts a proven claim into a false pass with no visible
symptom. The second keeps the database honest in the other direction: a run
measures one fixture's shape, and some actions are only required in shapes no
fixture can reach. `iam:DeletePolicyVersion` is the case — minimality proved it
droppable for a one-version update, and it is required once a policy hits AWS's
five-version cap. Demanding equality would force it out of the entry and ship
the false pass the measurement appeared to justify.

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
