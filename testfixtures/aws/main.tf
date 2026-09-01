# Contract-test fixtures for the M1 spike.
#
# These roles exist only so that iam:SimulatePrincipalPolicy has something with
# deliberately-shaped policies to evaluate. They are never assumed, never used
# to deploy anything, and grant no access to any real resource.
#
# Everything here is free: IAM roles and policies have no cost.
#
# See README.md in this directory for what each fixture is for and how to run
# the experiments against it.

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {}

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  p      = var.name_prefix
  part   = data.aws_partition.current.partition
  bucket = "arn:${local.part}:s3:::${var.name_prefix}"
}

# SimulatePrincipalPolicy ignores trust policies entirely — it reads the
# identity policies attached to the principal and nothing else. So rather than
# grant anyone a way into these roles, we make them effectively unassumable:
# an external ID nobody has a reason to use. Even if assumed, they permit only
# s3:CreateBucket on buckets that do not exist.
data "aws_iam_policy_document" "unassumable" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = [data.aws_caller_identity.current.account_id]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = ["preflight-contract-fixture-never-assumed"]
    }
  }
}

# --------------------------------------------------------------------------
# Fixture 1 — policy scoped to one exact ARN.
#
# The strictest case. Experiments E1 and E3 use this to find out whether a
# wildcard or partial-wildcard ResourceArns value can produce a spurious
# "allowed" against it. See docs/DESIGN.md §6.3.
# --------------------------------------------------------------------------
resource "aws_iam_role" "exact_arn" {
  name               = "${local.p}-exact-arn"
  assume_role_policy = data.aws_iam_policy_document.unassumable.json
  description        = "preflight contract fixture: policy scoped to one exact ARN"
}

resource "aws_iam_role_policy" "exact_arn" {
  name = "allow-exact-bucket"
  role = aws_iam_role.exact_arn.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "s3:CreateBucket"
      Resource = "${local.bucket}-exact-bucket"
    }]
  })
}

# --------------------------------------------------------------------------
# Fixture 2 — policy scoped to an ARN prefix.
#
# The common real-world shape: least-privilege policies almost always scope by
# prefix rather than enumerating every bucket.
# --------------------------------------------------------------------------
resource "aws_iam_role" "prefix_arn" {
  name               = "${local.p}-prefix-arn"
  assume_role_policy = data.aws_iam_policy_document.unassumable.json
  description        = "preflight contract fixture: policy scoped to an ARN prefix"
}

resource "aws_iam_role_policy" "prefix_arn" {
  name = "allow-prefix"
  role = aws_iam_role.prefix_arn.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "s3:CreateBucket"
      Resource = "${local.bucket}-*"
    }]
  })
}

# --------------------------------------------------------------------------
# Fixture 3 — allowed on any resource, but gated on a condition key.
#
# Experiments E6 and E7 use this to confirm the condition-key design in
# docs/DESIGN.md §7.2: that an unsupplied key both denies AND reports itself in
# MissingContextValues, so we can degrade confidence instead of reporting a
# false denial.
# --------------------------------------------------------------------------
resource "aws_iam_role" "condition_key" {
  name               = "${local.p}-condition-key"
  assume_role_policy = data.aws_iam_policy_document.unassumable.json
  description        = "preflight contract fixture: allow gated on aws:RequestedRegion"
}

resource "aws_iam_role_policy" "condition_key" {
  name = "allow-if-region-matches"
  role = aws_iam_role.condition_key.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "s3:CreateBucket"
      Resource = "*"
      Condition = {
        StringEquals = {
          "aws:RequestedRegion" = var.condition_region
        }
      }
    }]
  })
}

# --------------------------------------------------------------------------
# Fixture 4 — a role with an IAM path.
#
# Experiment E8. An assumed-role session ARN omits the path, so string parsing
# alone reconstructs the WRONG ARN for this role. This fixture proves whether
# that reconstruction fails loudly (NoSuchEntity) or silently, and therefore
# whether iam:GetRole is genuinely needed. See docs/DESIGN.md §5.1.
# --------------------------------------------------------------------------
resource "aws_iam_role" "pathed" {
  name               = "${local.p}-pathed"
  path               = "/preflight-contract-fixture/"
  assume_role_policy = data.aws_iam_policy_document.unassumable.json
  description        = "preflight contract fixture: role with a non-default IAM path"
}

resource "aws_iam_role_policy" "pathed" {
  name = "allow-exact-bucket"
  role = aws_iam_role.pathed.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "s3:CreateBucket"
      Resource = "${local.bucket}-exact-bucket"
    }]
  })
}

# --------------------------------------------------------------------------
# The policy the test runner needs.
#
# Deliberately scoped to the fixture roles rather than "*". SimulatePrincipalPolicy
# discloses the permissions of whatever principal it is pointed at, so granting
# it account-wide hands the holder a way to enumerate everyone's access. Attach
# this to whichever identity runs the contract tests.
# --------------------------------------------------------------------------
resource "aws_iam_policy" "contract_test_runner" {
  name        = "${local.p}-contract-test-runner"
  description = "Read-only permissions needed to run preflight's M1 contract tests"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "SimulateOnlyTheFixtureRoles"
        Effect = "Allow"
        Action = [
          "iam:SimulatePrincipalPolicy",
          "iam:GetContextKeysForPrincipalPolicy",
          "iam:GetRole",
        ]
        Resource = [
          aws_iam_role.exact_arn.arn,
          aws_iam_role.prefix_arn.arn,
          aws_iam_role.condition_key.arn,
          aws_iam_role.pathed.arn,
        ]
      },
      {
        Sid      = "IdentifySelf"
        Effect   = "Allow"
        Action   = "sts:GetCallerIdentity"
        Resource = "*"
      },
    ]
  })
}
