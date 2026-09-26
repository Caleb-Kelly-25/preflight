# Two-phase fixture: aws_iam_role UPDATE, varying the DESCRIPTION only.
#
# This is the fixture that should settle iam:UpdateRole vs
# iam:UpdateRoleDescription. IAM has BOTH, the provider has historically used
# UpdateRoleDescription for a description change, and the entry gates both on
# `description` because nobody had measured which one actually runs. Whichever
# this run does not need is retained over-reporting rather than fact.
#
# Phase 1 is the before state, applied by the harness with OPERATOR credentials.
# Phase 2 is the change, applied under the scratch role — that is the
# measurement. See derivefixtures/README.md for why the split exists.
#
# EVERYTHING ELSE IS CONSTANT ACROSS PHASES, deliberately. An update path depends
# on which attribute changed, so a fixture varying two produces one action set
# with no way to attribute any of it.
#
# Run:
#   make derive TYPE=aws_iam_role OPERATION=update #     FIXTURE=./derivefixtures/aws_iam_role__update_description

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

# 1 = the before state, established with operator credentials.
# 2 = the change under measurement, applied by the scratch role.
variable "phase" {
  type    = number
  default = 1

  validation {
    condition     = contains([1, 2], var.phase)
    error_message = "phase must be 1 (before) or 2 (the measured change)."
  }
}

resource "aws_iam_role" "probe" {
  name        = "preflight-derive-role-desc"
  description = var.phase == 1 ? "before" : "after"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = {
    Name = "preflight-derive-role-desc"
    env  = "derive"
  }
}
