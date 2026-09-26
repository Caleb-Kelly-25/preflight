# Fixture for deriving aws_s3_bucket_public_access_block's required IAM actions.
#
# The resource it acts on is NOT created here. It is a support resource created
# out of band and referenced by name:
#
#   s3 bucket preflight-derive-support-<account>
#
# That is a correctness requirement, not convenience. A fixture that created its
# own support resource would need that resource's actions too, and the
# derivation would attribute them to aws_s3_bucket_public_access_block — which
# would be wrong, because a derived set only means something if the fixture
# exercises exactly one resource type.
#
# Run: make derive TYPE=aws_s3_bucket_public_access_block FIXTURE=./derivefixtures/aws_s3_bucket_public_access_block

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

resource "aws_s3_bucket_public_access_block" "probe" {
  bucket                  = "preflight-derive-support-${var.account_id}"
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
