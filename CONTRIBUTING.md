# Contributing

## The most useful thing you can do

Verify a mapping entry. Every entry in `mappings/` is currently marked `draft`,
meaning nobody has checked it against AWS's own reference. Promoting one to
`verified` needs no Go — see [mappings/README.md](mappings/README.md).

Second most useful: run `preflight check` against a real plan from your own
infrastructure and open an issue with the resource types that came back
`Unchecked`. That tells us what to map next, using real usage rather than
guesswork. You do not need to share the plan itself — the resource type list is
enough, and it contains nothing sensitive.

## Development

Requires Go 1.25 or newer.

```
go build ./...
go test ./...
go vet ./...
gofmt -l .          # must print nothing
```

No test in this repo requires AWS credentials, and none should. Anything that
talks to AWS goes behind an interface and is faked in tests — see
`engine.Simulator`.

## Ground rules

**Never let an unverified check present as safe.** This is the one rule the
project does not bend on. If a code path cannot fully verify something, it must
produce `Likely` or `Unchecked` with a stated reason. A PR that makes a finding
look more confident than the evidence supports will be rejected on that basis
alone, however much it improves the numbers.

**False positives matter too.** A spurious red check trains a team to bypass the
tool, which is worse than the tool not existing. If a change might over-report
required permissions, say so in the PR.

**Coverage stays free.** Nothing that gates *whether* something gets checked
behind payment will be merged.

## Pull requests

- One logical change per PR.
- Add a test. For mapping changes, `TestShippedDatabase` covers the structural
  checks automatically; explain in the PR how you verified the action list.
- Update `docs/ARCHITECTURE.md` if you close or change one of the open questions.
- Commits in the imperative mood: "add ECS mappings", not "added ECS mappings".

## Cutting a release

Maintainers only, and the order matters.

1. Update `VERSION` to the bare version — `0.2.0`, no `v`. `action.yml` reads
   that file to decide which release to download when a caller does not pin one,
   so a tag that disagrees with it would make `uses: ...@v0.2.0` fetch some
   other binary. The release workflow refuses to publish that mismatch rather
   than leaving it to be discovered in someone else's CI.
2. Merge it.
3. Tag and push: `git tag v0.2.0 && git push origin v0.2.0`.

`.github/workflows/release.yml` then re-runs the full test suite, checks the
tag against `VERSION`, and only afterwards — in a separate job, the only one
holding `contents: write` — runs GoReleaser to cross-compile, publish the
archives and publish `checksums.txt`. That checksum file is not decoration: the
Action refuses to execute a binary it does not vouch for.

`.goreleaser.yaml` and `action.yml` are a matched pair. The archive naming in
one is reconstructed by the other, so renaming an archive breaks every workflow
using the Action. CI runs `goreleaser check` and a snapshot build on every pull
request to keep that from being discovered at tag time.

## Reporting a problem

For a **wrong mapping**, include the resource type, the action list you expected,
and how you determined it — ideally the Service Authorization Reference URL or
the provider source line.

For a **false positive** (preflight said denied, apply succeeded), include the
resource type, the action reported missing, and the shape of the policy that
allowed it. Redact ARNs and account IDs freely; the structure is what matters.

Please do not paste raw plan output, real ARNs, or account IDs into public
issues.

## Security

Do not open a public issue for a security problem. Email the maintainer instead.
