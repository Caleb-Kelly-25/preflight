# MAXIMAL create fixture for aws_s3_bucket — THE HARNESS'S OWN REGRESSION TEST.
#
# This fixture exists for a different reason from every other one here. The others
# measure a mapping; this one measures the HARNESS.
#
# aws_s3_bucket is the only entry in the database whose true answer was established
# independently: a hand-run on 2026-09-01 against provider 5.100.0 found that a role
# holding exactly 17 actions can apply AND destroy a tagged bucket, and that removing
# any one of them fails. That set is ground truth the harness did not produce, so
# re-deriving it is the only available check that the harness's answers can be
# trusted at all.
#
# The plan named this as Phase 2's exit criterion — "run the harness against
# aws_s3_bucket and confirm it independently re-derives the known-correct set" — and
# it had never been run. There was no maximal S3 fixture; the hand-run predated the
# harness, so there was nothing to commit. Sixteen mapping corrections were made on
# the harness's word before anyone checked it against the one case with an
# independent answer.
#
# WHAT TO EXPECT. 16 actions on create (2 writes + 14 read-backs) and 17 across
# create and delete, which is what the entry's notes mean by "17 actions applies and
# destroys" — the number spans both operations and reads like a discrepancy against
# the create-only evidence. Anything else is a finding, in either direction.
#
# MINIMISATION IS EXPECTED TO STALL. Removing s3:ListBucket does not deny: the
# provider retries HeadBucket until the apply times out. That was discovered by the
# hand-run, it is why OutcomeStalled exists, and the harness now treats a stall as
# evidence and stops the run rather than measuring against a possibly-dirty account.
# So this may take more than one run to minimise fully — sweep and re-run.
#
# TAGGED ON PURPOSE. s3:PutBucketTagging is in the create path because CreateBucket
# does not accept tags, so a tagged bucket makes a second call. An untagged bucket
# needs one fewer action, which is what derivefixtures/aws_s3_bucket__minimal
# measures.
#
# force_destroy is deliberately NOT set. It pulls in s3:DeleteObject and the entry
# says so; adding it here would change the delete path being measured.
#
# Run: make derive TYPE=aws_s3_bucket FIXTURE=./derivefixtures/aws_s3_bucket

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

resource "aws_s3_bucket" "probe" {
  # Bucket names are globally unique, so the account id keeps this from colliding
  # with another contributor's run.
  bucket = "preflight-derive-bucket-${var.account_id}"

  tags = {
    Name = "preflight-derive-bucket"
    env  = "derive"
  }
}
