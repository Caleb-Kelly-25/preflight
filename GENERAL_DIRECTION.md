# Strategy & Direction

*Companion to the technical MVP spec. This document changes rarely — it holds the reasoning behind decisions, not the decisions' current implementation state. When a new situation isn't covered by the technical spec, resolve it against the principles here first.*

## 1. The problem, specifically

Terraform's `plan` step succeeding tells you the configuration is internally valid — not that the identity running `apply` can actually execute it. Permission failures surface only after merge, mid-deploy, discovered one error at a time through iterative fix-and-rerun cycles. This is not a hypothetical: it's a documented, six-plus-year-old unresolved gap (two separate open feature requests on HashiCorp's own Terraform AWS provider, from 2017 and 2019), and it's a gap the founding team hit directly, losing a full day to manually bisecting IAM errors across coordinated cross-repo deploys.

**Why now, specifically:** not a market trend claim — a persistence claim. This gap has stayed open for years despite the provider's own maintainers acknowledging it, because closing it well requires an always-current mapping between ~1,400+ resource types and AWS's IAM action surface — real, ongoing maintenance work that hasn't been anyone's clear job. That's the opening: not that the problem is new, but that nobody has committed to owning its upkeep.

## 2. Business model

Open-core, following the structure Bridgecrew used with Checkov (acquired by Palo Alto Networks for $156M, roughly two years after founding) — a proven shape in this exact category, not a novel bet.

- **Free, permanently**: the CLI and GitHub Action. No login, no telemetry by default, works in any CI.
- **Paid**: the platform layer teams need once they outgrow a single repo — dashboards, historical trends, org-wide policy templates, and deeper verification (see §3).

## 3. Monetization philosophy — depth, not breadth

**The rule:** never charge for *whether* something gets checked. Only ever charge for *how deeply* it gets checked, or for *aggregating* checks across many repos and teams.

Resource-type coverage (S3, EC2, RDS, and so on) stays free and expands toward completeness over time, publicly tracked. Gating coverage by paywall would mean a team using a resource type outside the free tier silently gets a false sense of safety — precisely the failure mode the confidence model exists to prevent. The tool's entire credibility rests on "checked" meaning checked, for everyone, always.

What legitimately justifies payment: verification that requires deeper trust and infrastructure access than a single CI role's own permissions — specifically, resolving the "Likely" confidence state (see technical spec §5) by analysing resource-based policies (S3 bucket policies, KMS key policies, and so on) and AWS Organizations resource control policies, which requires cross-account visibility most teams reasonably expect to pay for rather than grant lightly. Gate by depth of access required, never by basic coverage.

**Correction (2026-08-31).** This section previously named AWS Organizations SCPs alongside resource-based policies as what justifies payment. That is not accurate: `iam:SimulatePrincipalPolicy` evaluates in-scope SCPs server-side — including their condition keys and resource scoping — requiring no caller permission beyond the simulate call itself. **SCP checking is free, and under §3's own rule it must stay free.** The remaining justification is narrower but stands on firmer ground: resource-based policies cannot be simulated for IAM roles at all, because the API refuses, and CI deploys always use roles. Evaluating them therefore means building an IAM evaluation engine rather than delegating to AWS's — more work, and a harder asset to clone. Full reasoning and sources: `docs/DESIGN.md` §0.

## 4. Competitive positioning, and the reasoning behind it

- **Checkov / Prisma Cloud is not a competitor.** They check whether an IAM policy is *written* safely (a security/compliance question, sold to a security buying center). This tool checks whether the *deploying identity* can actually execute a given plan (a reliability/deployability question, sold to a platform/SRE buying center). Different axis, different buyer — but the same company, so treat any signal of them extending into this space as an actual event to react to, not noise.
- **HashiCorp is the real absorption risk**, not a hypothetical one — they've already closed an adjacent gap natively once (Terraform Stacks, for cross-repo orchestration). The likely mitigant is speed and focus on a narrow, unglamorous problem that a platform company's roadmap may not prioritize at their scale — not structural safety. Assume no permanent immunity.
- **This is not the cross-repo orchestration problem** (that's Terragrunt / Atmos / HCP Terraform Stacks' territory, already crowded, already includes a native first-party answer). Position as complementary to those tools, never as a competitor to them.

## 5. Licensing reasoning

- **Core CLI/engine: permissive (MIT/Apache 2.0), deliberately.** The adoption wedge requires zero legal-review friction — a source-available license with usage restrictions reintroduces exactly the friction the free tier exists to eliminate.
- **Mapping database content: open**, for trust and community maintenance — verifiable coverage claims and community-contributed fixes are worth more than the marginal protection from closing it.
- **The automated pipeline that keeps the database current: proprietary.** This is the actual hard-to-clone asset — not the code, the ongoing maintenance discipline.
- **Standing rule for any future component**: default to open at MVP stage; revisit toward source-available or proprietary only once there's real revenue or a live cloning threat to protect against, not preemptively. HashiCorp's 2023 BUSL relicensing happened *after* competitors monetized their free code without contributing back — react to that pattern if and when it appears, don't pre-empt it at the cost of adoption now.

## 6. Standing decision principles (apply these to situations not yet covered elsewhere)

1. **If a check cannot be fully verified** (unmapped resource type, resource-based policy or RCP involved, resource ARN unknown until apply, condition-key values unavailable, mapping entry unverified) **→ label it visibly as such.** Never let an unverified check present as "safe."
2. **If a new feature would gate basic coverage behind payment → don't ship it.** Gate depth or aggregation instead.
3. **If a feature requires data to leave the user's environment → it must be an explicit, visible opt-in, never a default.** This tool already asks for real AWS credential access; don't compound that trust ask silently.
4. **If unsure whether to open-source a new component → default to open.** Revisit only against real revenue or a real competitive threat, not speculatively.
5. **If a competitor or platform vendor moves into adjacent territory → treat it as real information and revisit positioning**, not as a reason to panic or as something to ignore.

## 7. Expansion paths (options, not commitments)

Roughly in order of how directly they extend the core asset: broaden beyond IAM to general apply-time preflight failures (quota limits, naming collisions); multi-cloud, GCP more plausible than Azure since Azure already has a native answer; multi-IaC-tool (Pulumi, CDK, CloudFormation share the same underlying gap); integration hooks into Terragrunt/Atmos/HCP Terraform Stacks as a complementary layer; upmarket compliance/audit reporting once core adoption is proven. Each requires its own validation before being treated as real demand — this list is a menu, not a roadmap.

## 8. Exit context — plausible paths, not a plan

Worth naming explicitly: this section describes what *could* happen if the business succeeds, not a timeline to build toward. Treating an exit date as a target risks optimizing decisions for looking acquirable instead of building something that earns it.

Plausible paths, roughly by likelihood: strategic acquisition by a cloud security/DevSecOps platform (the Bridgecrew-shaped outcome); strategic acquisition by an IaC orchestration platform wanting to bundle this as a verification layer; a sustainable independent business with no exit at all, which is a legitimate outcome, not a fallback. Bridgecrew's two-year timeline was an exceptional case — a uniquely connected founding team, real capital, and no adjacent incumbent already in the workflow. This company doesn't currently share all of those conditions. A more honest planning assumption is that real traction, not a clock, is what makes any of these paths available at all.

## 9. Known open risks (stated honestly, not hidden)

- **DIY-ability**: any team can build a narrower, internal-only version themselves. The product's value is convenience and completeness, not impossibility to replicate — a real but weaker moat than infeasibility would be.
- **Unverified demand breadth**: confirmed as a real, multi-year pain for the founding team and documented in public feature requests — not yet confirmed as broadly felt across many teams. Resolving this is the current top priority (see: talk to 5-10 outside engineers before further build investment).
- **Incumbent absorption**: real and not fully mitigable — see §4.