# Fixture for deriving aws_s3_bucket_policy's required IAM actions.
#
# The resource it acts on is NOT created here. It is a support resource created
# out of band and referenced by name:
#
#   s3 bucket preflight-derive-support-<account>
#
# That is a correctness requirement, not convenience. A fixture that created its
# own support resource would need that resource's actions too, and the
# derivation would attribute them to aws_s3_bucket_policy — which
# would be wrong, because a derived set only means something if the fixture
# exercises exactly one resource type.
#
# Run: make derive TYPE=aws_s3_bucket_policy FIXTURE=./derivefixtures/aws_s3_bucket_policy

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

variable "account_id" {
  description = "Account the fixture runs in. Supplied by the harness as TF_VAR_account_id, so no account number is ever written into this repository."
  type        = string
}

provider "aws" {}

resource "aws_s3_bucket_policy" "probe" {
  bucket = "preflight-derive-support-${var.account_id}"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "InertDeny"
      Effect    = "Deny"
      Principal = { AWS = "arn:aws:iam::${var.account_id}:role/preflight-derive-support" }
      Action    = "s3:GetObject"
      Resource  = "arn:aws:s3:::preflight-derive-support-${var.account_id}/never-used/*"
    }]
  })
}
