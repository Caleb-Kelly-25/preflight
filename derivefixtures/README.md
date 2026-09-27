# Derivation fixtures

Each directory here is one **measurement**: a minimal Terraform configuration the
derivation harness applies under a scratch role holding exactly a candidate set
of IAM actions, in order to find out which actions that configuration genuinely
requires.

These fixtures **create real AWS resources**. They are not tests. `make test`
never reaches them; running one takes the `awsderive` build tag and three
separate guards. See `CLAUDE.md` for the harness and `cmd/derive/main.go` for the
guards.

## Why fixtures exist at all

A mapping entry claims "creating an `aws_iam_role` requires these seven actions."
That claim has two dimensions, and only one is checkable by inspection:

- **Names correct** — `accessanalyzer:ValidatePolicy` settles this, for free.
- **List complete** — nothing settles this but running a real apply with a
  deliberately incomplete permission set and watching it fail.

Completeness is the dimension that produces a false "allowed", so it is the
dimension that needs the money and the blast radius. Every entry whose create
path has been derived so far was found to be **wrong, always by omission** —
ten for ten.

## Naming

| Pattern | Means |
|---|---|
| `aws_vpc/` | The maximal create fixture: tags set, optional attributes set. |
| `aws_vpc__minimal/` | The same type with nothing optional set. |
| `aws_iam_policy__update/` | A two-phase fixture measuring an update path. |
| `aws_iam_role__update_tags/` | An update fixture naming the attribute it varies. |

Once a type has more than one update fixture, name each after the attribute it
varies — `__update_description`, `__update_tags`. A bare `__update` is fine only
while there is one.

The harness takes a directory, so a variant is just another directory. No
registry, no index — the name carries the meaning.

### Why variants are load-bearing

A derived set is valid for **the configuration the fixture uses**, not for the
resource type in general. A tagged bucket needs `s3:PutBucketTagging`; an
untagged one does not.

That is what the schema's `when` gates express, and a gate is the only schema
feature that can introduce a **false pass**, because it *removes* actions. A gate
can therefore only be trusted if it was measured in **both** directions: derive
the maximal fixture, derive the minimal one, and the difference between the two
sets *is* the gate. If an untagged VPC still requires `ec2:CreateTags`, the gate
is wrong and the entry is under-reporting today.

This method found a shipped false pass: `ec2:RevokeSecurityGroupEgress` was gated
on `attribute_set: egress` and is required *without* it, because the provider
manages egress exhaustively and revokes the allow-all rule AWS attaches to every
new group.

## Create fixtures

One resource, the smallest configuration that is still representative. Conventions:

- **Name every resource `preflight-derive-*`.** The sweep that cleans up after a
  crashed run finds resources by that prefix and by the `preflight-derive` tag.
- **Set tags.** Tagging has needed its own permission in every service measured,
  by three *different* mechanisms — S3 makes a separate `PutBucketTagging` call,
  while IAM and EC2 authorize the tagging action as part of the create with no
  separate call at all. An untagged fixture misses it, and which mechanism a
  service uses is not inferable from another service.
- **Keep anything the fixture grants inert.** Policy documents `Deny` an action
  on a resource that does not exist. A fixture must not be able to hold
  privileges even if someone attaches it.
- **Take `account_id` as a variable** if the configuration needs an ARN. The
  driver sets `TF_VAR_account_id` from the verified caller identity. Do not
  hardcode an account number — it ends up in git history.

## Two-phase fixtures: measuring an update

An update cannot be measured by a create fixture, because there is nothing to
update. The resource has to exist first — and it has to be created with **full
permissions**, or the run measures the create and the update together with no way
to tell them apart.

So a two-phase fixture declares:

```hcl
variable "phase" {
  type    = number
  default = 1
}
```

and the harness runs it twice per attempt:

| Phase | Credentials | What it is |
|---|---|---|
| 1 | operator | The before state. Not measured. |
| 2 | scratch role | The change. This is the measurement. |

`cmd/derive` **refuses to run** a non-create operation against a fixture that
declares no `phase` variable. Without it both phases apply the same
configuration, the scratch role performs the create, and the run reports the
create path under the name "update" — a wrong mapping presented as measured,
which is the one failure this tool exists to prevent.

### Change exactly one thing

An update path depends on *which attribute changed*, so a fixture that changes
three attributes produces one action set and no way to attribute any of it. Hold
everything else constant, including tags.

### Do not change an attribute that forces a replace

Terraform replaces rather than updates when an immutable attribute changes, and a
replace is a create plus a delete wearing an update's name. Check the provider
docs for `ForceNew` before picking the attribute to vary. `aws_iam_policy`'s
`name` and `description` are both in that category.

## Support fixtures: dependencies the measured type needs

Some types cannot be measured alone. An inline role policy needs a role, a log
stream needs a log group, an SQS queue policy needs a queue.

**Do not create the dependency inside the measured fixture.** That is a correctness
bug, not a shortcut: the apply would also need the dependency's own create actions,
the derivation would discover them, and they would land in your resource type's
action list although they belong to another type entirely. A derived set only means
anything if the fixture exercises exactly one resource type.

Put it in a separate directory under `derivefixtures/support/` and pass it with
`SUPPORT=`:

```
make derive TYPE=aws_sqs_queue_policy   FIXTURE=./derivefixtures/aws_sqs_queue_policy   SUPPORT=./derivefixtures/support/sqs_queue
```

The harness applies it **once per run with operator credentials**, before the loop,
and destroys it after — after the measured fixture, since the measured resource
depends on it. The scratch role never applies it and never needs permission for it.

Conventions:

- **Name support resources `preflight-derive-support-*`** and refer to them from the
  measured fixture by that fixed name. Passing Terraform outputs between two
  separate workspaces would need remote state or a wrapper; a documented constant is
  simpler and a maintainer tool can afford it.
- **Keep whatever the measured resource writes permissive.** A deny-all policy on a
  support queue or bucket can deny the harness's own operator teardown, which aborts
  the run and leaks the resource.
- One support fixture can serve several measured fixtures. Add resources to an
  existing one rather than duplicating it.

## Delete paths need no fixture of their own

A delete reuses the type's create fixture. The harness applies it with **operator**
credentials, then destroys it under the **scratch role** — and that destroy is the
measurement:

```
make derive TYPE=aws_iam_role OPERATION=delete FIXTURE=./derivefixtures/aws_iam_role
```

So every entry that already has a create fixture can have its delete path measured
with no new files, and adding a create fixture buys two measurements rather than
one.

This was believed impossible until 2026-09-26. The harness had one `Destroy` doing
two jobs — proving the action set sufficient, and guaranteeing the account ends
clean — and because the second requires operator credentials, the first never ran
under the scratch role. Splitting them cost one method.

The operator cleanup still runs after every measured destroy, including the denied
ones. Those are the attempts where a resource is guaranteed to be left standing.

## Traps already paid for

- **A provider `default_tags` block silently tags a "minimal" fixture**, so the
  minimal variant measures tagging anyway and the gate appears to hold when it
  was never tested.
- **`attribute_set` reads a `false` boolean as unset.** `aws_vpc`'s
  `enable_dns_support` defaults to *true*, so the meaningful non-default value is
  exactly the one the gate treats as absent — gating on it would have been a
  false pass. `aws_subnet`'s boolean gate is correct only because that attribute
  happens to default to false.
- **`ec2:DescribeTags` has been a surplus guess twice.** The provider reads tags
  back from each resource's own `Describe` call. Stop adding it.
- **An entry with no `read_actions` has under-reported every single time it was
  measured — ten for ten.** Terraform reads every resource back after writing
  it, so an empty read set almost always means nobody checked.
- **Some denials hang rather than fail.** A missing `s3:ListBucket` makes the
  provider retry `HeadBucket` indefinitely. The harness treats a stall as
  evidence, kills the whole process tree, and then **stops the run**, because a
  killed apply may have created a resource it never recorded in state.
- **EC2 and VPC do not name the denied action.** They return
  `UnauthorizedOperation` with an opaque encoded message. Decoding needs
  `sts:DecodeAuthorizationMessage` and does not always work.

## Adding a fixture

1. Write the directory, following the conventions above.
2. Run `go run ./cmd/arncheck` if you also touched a mapping — it verifies every
   `arn_format` against AWS's machine-readable service reference.
3. Run the harness and read the report. It deliberately does **not** edit the
   YAML: promoting an entry to `verified` is a *claim*, and a person makes it in
   the pull request.
4. Commit the evidence file the run wrote to `mappings/evidence/<type>.json`.
   That part is not a claim, it is a transcript — the measured action set, the
   fixture, the date, and the AWS provider version read out of the lock file
   Terraform actually wrote. `TestVerifiedEntriesHaveEvidence` checks every
   verification claim against it, and deleting a proven action from a verified
   entry fails CI because of it.

   Do not hand-write or hand-edit these. The eleven files that were backfilled
   from prose notes are marked `backfilled: true`, and the first hand-written
   provenance in them was already wrong: three runs recorded as provider 6.63.0
   had really run on 6.66.0. If provenance can be computed, do not type it.
