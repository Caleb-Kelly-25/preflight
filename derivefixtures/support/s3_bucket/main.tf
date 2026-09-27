# SUPPORT fixture: an S3 bucket for the aws_s3_bucket_* sub-resources to configure.
#
# NOT MEASURED. Applied once with OPERATOR credentials before the loop and destroyed
# after; the scratch role never applies it. See derivefixtures/README.md.
#
# Serves four measured fixtures, which is why it is one stack rather than four:
#
#   aws_s3_bucket_policy
#   aws_s3_bucket_versioning
#   aws_s3_bucket_public_access_block
#   aws_s3_bucket_server_side_encryption_configuration
#
# Each of those acts on a bucket that ALREADY EXISTS, which is the whole reason they
# are separate entries from aws_s3_bucket: their create can be denied by the bucket's
# own policy, so they declare resource_policy_capable on create where the bucket
# itself is exempt.
#
# Creating the bucket inside a measured fixture would pull s3:CreateBucket and the
# fourteen read-backs into the measurement and attribute them to a sub-resource that
# needs none of them. That is not hypothetical: aws_s3_bucket's create needs 17
# actions, and every one would have landed in the wrong entry.
#
# REPLACES A HAND-RUN STEP. Until 2026-09-26 the four fixtures documented an
# `aws s3 mb` command for the maintainer to run first. That worked and was not
# reproducible.

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

variable "account_id" {
  description = "Set by the harness from the verified caller identity."
  type        = string
}

resource "aws_s3_bucket" "support" {
  # Bucket names are globally unique, so the account id keeps this from colliding
  # with another contributor's run.
  bucket = "preflight-derive-support-${var.account_id}"

  # force_destroy because aws_s3_bucket_versioning's measurement leaves the bucket
  # versioned: destroying it then has object versions to remove, and a teardown that
  # cannot finish aborts the run and leaks a bucket.
  force_destroy = true
}
