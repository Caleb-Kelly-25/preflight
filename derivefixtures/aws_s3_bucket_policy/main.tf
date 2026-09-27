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
      Sid    = "InertDeny"
      Effect = "Deny"
      # The ACCOUNT ROOT, not a role. Naming a role made this fixture depend on two
      # support resources instead of one, and AWS rejects a bucket policy whose
      # principal does not exist ("MalformedPolicy: Invalid principal") — which is
      # exactly how the delete derivation failed on 2026-09-27 once the bucket came
      # from a support fixture that creates no role.
      #
      # Still inert: it DENIES a read on a path nothing uses, so it grants nobody
      # anything and cannot deny the operator teardown either.
      Principal = { AWS = var.account_id }
      Action    = "s3:GetObject"
      Resource  = "arn:aws:s3:::preflight-derive-support-${var.account_id}/never-used/*"
    }]
  })
}
