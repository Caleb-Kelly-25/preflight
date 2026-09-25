# Fixture for deriving aws_iam_role_policy's required IAM actions.
#
# The role is NOT created here. It is a support resource, created out of band
# before the run and destroyed after:
#
#   aws iam create-role --role-name preflight-derive-support ...
#
# That matters for correctness rather than convenience. If the fixture created
# the role, the apply would also need iam:CreateRole, iam:GetRole, iam:DeleteRole
# and the rest — the derivation would discover them, and they would land in
# aws_iam_role_policy's action list even though they belong to a different
# resource type entirely. A derived set is only meaningful if the fixture
# exercises exactly one resource type.
#
# The cost is that the fixture is not self-contained: it fails unless the
# support role already exists. That is the right trade for a maintainer tool.
#
# Run via:
#   make derive TYPE=aws_iam_role_policy FIXTURE=./derivefixtures/aws_iam_role_policy

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # No default_tags: an inline role policy takes no tags, and a stray provider
  # default would only add noise.
}

resource "aws_iam_role_policy" "probe" {
  name = "preflight-derive-inline"
  role = "preflight-derive-support"

  # Deliberately inert: a Deny on a resource that does not exist. This grants
  # nothing to anyone.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = "s3:GetObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })
}
