# Fixture for deriving aws_iam_policy's required IAM actions.
#
# Tagged on purpose. Tagging has needed its own permission in all three services
# measured so far, by three different mechanisms — S3 makes a separate
# PutBucketTagging call, while IAM and EC2 authorize the tagging action as part
# of the create with no separate call at all. An untagged fixture would miss it.
#
# The policy document itself is deliberately inert: it grants a read-only action
# on a resource that does not exist. Nothing here is assumable or usable; the
# point is to exercise the aws_iam_policy resource lifecycle, not to hold
# permissions.
#
# Run via: make derive TYPE=aws_iam_policy FIXTURE=./derivefixtures/aws_iam_policy

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

resource "aws_iam_policy" "probe" {
  name        = "preflight-derive-policy"
  description = "preflight mapping derivation; safe to delete"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = "s3:GetObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })

  tags = {
    Name = "preflight-derive-policy"
    env  = "derive"
  }
}
