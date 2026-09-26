# MINIMAL variant of the aws_s3_bucket fixture, existing to MEASURE A `when` GATE.
#
# `when` is the only schema feature that can introduce a false pass, because it
# removes actions from a requirement list. A gate is only reasoning until the
# negative case has been run: deriving the same type from a maximal fixture and
# from this one, the difference between the two derived sets IS the gate.
#
#   derivefixtures/aws_s3_bucket                     maximal  -> gated action required
#   derivefixtures/aws_s3_bucket__minimal            -> should NOT be
#
# NOTE THE ABSENT default_tags. The maximal fixtures set one so leaked resources
# can be swept by tag, but default_tags applies to EVERY resource in the
# configuration — a minimal fixture that keeps it silently tags the resource and
# measures nothing. That mistake was nearly shipped once.
#
# Run: make derive TYPE=aws_s3_bucket FIXTURE=./derivefixtures/aws_s3_bucket__minimal

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

provider "aws" {
  # Deliberately no default_tags. See above.
}

resource "aws_s3_bucket" "probe" {
  bucket = "preflight-derive-min-${var.account_id}"
}
