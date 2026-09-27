# SUPPORT fixture: an IAM role and a managed policy, for the aws_iam_* sub-resources.
#
# NOT MEASURED. Applied once with OPERATOR credentials and destroyed after the run.
#
# Serves three measured fixtures:
#
#   aws_iam_role_policy             attaches an inline policy to the role
#   aws_iam_role_policy_attachment  attaches the managed policy to the role
#   aws_iam_instance_profile        adds the role to a profile (needs iam:PassRole)
#
# Creating the role inside a measured fixture would pull iam:CreateRole, iam:GetRole,
# iam:DeleteRole, iam:TagRole and the two list calls into the measurement and
# attribute them to whichever sub-resource was under test. A derived set only means
# anything if the fixture exercises exactly one resource type.
#
# REPLACES A HAND-RUN STEP: these fixtures used to document an
# `aws iam create-role --role-name preflight-derive-support` command for the
# maintainer to run first.
#
# Both resources are deliberately inert. The role trusts only EC2 and grants nothing;
# the policy DENIES a read on a bucket that does not exist. Neither can hold privilege
# even if something attaches them.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  default_tags {
    tags = {
      preflight-derive = "true"
    }
  }
}

resource "aws_iam_role" "support" {
  name = "preflight-derive-support"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_policy" "support" {
  name        = "preflight-derive-support-policy"
  description = "preflight derivation support; safe to delete"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = "s3:GetObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })
}
