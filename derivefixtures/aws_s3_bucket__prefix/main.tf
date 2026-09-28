# aws_s3_bucket with bucket_prefix, for the DELETE loop.
#
# WHY A SECOND FIXTURE RATHER THAN CHANGING THE FIRST. derivefixtures/aws_s3_bucket
# is the harness's regression test: its value is that it reproduces the 2026-09-01
# hand-run exactly, so it keeps that run's fixed bucket name and is not changed to
# suit a different loop.
#
# But a fixed name cannot survive a DELETE derivation. That loop is
# create-with-operator, destroy-under-scratch, cleanup, repeat — so it recreates the
# same bucket name every attempt, and S3 will not reliably let you recreate a bucket
# name immediately after deleting it. The delete run on 2026-09-27 proved sufficiency
# and then failed minimisation at attempt 16 with a setup failure, for that reason
# and not for any permission reason.
#
# This is the same shape as the SQS 60-second cooldown, and the same fix: use the
# provider's own prefix feature so every apply gets a fresh name, rather than a trick
# like uuid() in the name which makes plans non-deterministic.
#
# It also exercises `arn_prefix_attributes` against a real apply for S3 — the entry
# maps BucketName to bucket_prefix and nothing had ever run it. That field builds a
# representative ARN when the real name is generated at apply time, which is never
# exact and so caps findings at Likely; it beats "*", which matches no ARN-scoped
# policy at all.
#
# Tagged, matching the regression fixture, so the derived set is comparable.
#
# Run: make derive TYPE=aws_s3_bucket OPERATION=delete \
#        FIXTURE=./derivefixtures/aws_s3_bucket__prefix

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

resource "aws_s3_bucket" "probe" {
  # Generated per apply, which is the whole point. No account suffix needed: the
  # random component already makes it globally unique.
  bucket_prefix = "preflight-derive-pfx-"

  tags = {
    Name = "preflight-derive-bucket-prefix"
    env  = "derive"
  }
}
