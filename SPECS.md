# IAM Preflight CLI + GitHub Action — MVP Spec (v1)

## 1. Problem statement

`terraform plan` succeeds in CI, but `terraform apply` fails after merge because the CI role lacks a specific IAM permission — discovered only after a real deploy attempt, costing CI time and forcing iterative fix-and-retry cycles. No existing tool statically predicts this before merge without either hand-authored simulation blocks or a real apply attempt.

## 2. Goals

- Catch IAM permission gaps between a proposed Terraform plan and the actual deploying principal's permissions, **before merge, without ever touching real AWS resources.**
- Zero-friction adoption: no login, no account, works in any CI, fully offline by default.
- Be honest about coverage — never silently pass a check it can't actually verify.

## 3. Non-goals (v1)

- Not a general IaC security/compliance scanner (that's Checkov/Prisma's job — different axis, not competing).
- Not a cross-repo orchestration tool (that's Terragrunt/Atmos/HCP Terraform Stacks' job).
- Not multi-cloud, not multi-IaC-tool, not full 1,400+ AWS resource type coverage. Deliberately narrow.

## 4. Core pipeline

1. **Input**: `terraform plan -out=plan.tfplan && terraform show -json plan.tfplan`
2. **Principal resolution**: resolve the *effective* deploying identity — including GitHub Actions OIDC-assumed roles, not just the base credential — via `aws sts get-caller-identity` + `aws_iam_session_context`-equivalent logic.
3. **Action mapping**: for each planned resource create/update/delete, map to the specific IAM actions AWS requires, using a maintained mapping database (Policy Sentry-style CRUD/ARN mapping as the technical foundation).
4. **Simulation**: batch mapped actions into `iam:SimulatePrincipalPolicy` calls (respecting API batch limits), executed read-only — no resources created, modified, or destroyed.
5. **Confidence classification** (see §5) and report generation.
6. **CI gate**: non-zero exit code on any confirmed gap, configurable threshold for "Likely" vs "Unchecked" states.

## 5. Confidence model (critical — do not ship binary pass/fail)

Every checked resource change gets one of three states:

- **Verified** — action(s) fully simulated against the identity policy and any in-scope SCPs, with nothing left unevaluated: a resolvable resource ARN, no resource-based policy or RCP in play, no missing condition-key values, and a verified mapping entry.
- **Likely** — identity-policy simulation passed, but something that could still deny at apply time was not evaluated. The reason is always named. *Permanent cases:* resource-based policies (S3 bucket policy, KMS key policy, etc.), which `iam:SimulatePrincipalPolicy` refuses to simulate for IAM roles — and CI deploys always use roles — plus AWS Organizations resource control policies (RCPs), which the simulator does not support. *Temporary cases:* a resource ARN not derivable from the plan, condition keys whose values could not be supplied, and mapping entries not yet verified. Flagged explicitly, not hidden.
- **Unchecked** — resource type not yet in the mapping database. Never silently treated as passing.

This is the single most important design principle in this spec: a false "safe" is more damaging to trust than a visible "not checked."

**Note on SCPs (corrected 2026-08-31).** An earlier draft of this section listed AWS Organizations SCPs as not reliably simulatable. They are: `iam:SimulatePrincipalPolicy` evaluates in-scope SCPs server-side, including condition keys and resource scoping, with no additional caller permission. **SCP presence therefore does not by itself downgrade a result to Likely.** It does mean SCP-driven denials arrive with no diagnostic — the API deliberately withholds matched statements and missing context values for SCPs — which is a false-positive risk documented in `docs/DESIGN.md` §7.3.

## 6. MVP resource scope

Initial mapping database covers the highest-frequency AWS resource types only:
S3, EC2, IAM, RDS, Lambda, VPC/networking, ECS.

Explicitly out of scope for v1; revisit based on real usage data.

## 7. Output formats

- **SARIF** — GitHub's native code-scanning format, renders as inline PR annotations directly in GitHub's UI. Primary output for CI integration.
- **Human-readable CLI report** — for local runs, shows exactly which resource change requires which action(s) and which are missing, grouped by confidence state.
- **Machine-readable JSON** — for teams building their own dashboards or integrating with other tooling.

## 8. Distribution & licensing

- **Core CLI/engine**: MIT or Apache 2.0. Fully permissive — this is the adoption wedge and needs zero legal-review friction.
- **Mapping database (content)**: open, inspectable, community-contributable — necessary for keeping ~1,400+ potential resource types current over time, and builds trust that coverage claims are verifiable.
- **Automated update pipeline** (the system that keeps the database current against
  new Terraform AWS provider releases and AWS IAM changes): not part of this
  repository.
- The core CLI and the mapping content are Apache 2.0. Licensing beyond that is
  recorded in GENERAL_DIRECTION.md (untracked).

## 9. Privacy / telemetry stance

- **Fully offline by default in the free tier.** No data leaves the CI environment unless explicitly opted in.
- Any future cloud sync is an explicit, visible opt-in — never a background default. This tool already requires read access to real AWS credentials; asking for additional implicit trust on top of that is a bigger ask than a typical static scanner makes, and should be treated accordingly.

## 10. Explicitly deferred to v1.x / v2 (not MVP)

- Auto-posted inline PR comments with suggested minimal policy patches.
- ~~SCP-aware simulation.~~ **No longer deferred — AWS does this for us, free.**
  See §5's note on SCPs.
- Resource-based policy analysis (S3 bucket policies, KMS key policies).
- Multi-cloud (GCP most plausible next target — Azure already has a native
  equivalent).

<!-- Commercial scope, success metrics and go-to-market moved to
     GENERAL_DIRECTION.md (untracked) on 2026-09-29. This file is the technical spec;
     what is free versus paid, and why, is a business decision that does not
     belong in a public engineering document. -->

