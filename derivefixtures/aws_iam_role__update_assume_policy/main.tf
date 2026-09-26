# Two-phase fixture: aws_iam_role UPDATE, varying the TRUST POLICY only.
#
# The trust policy is the security-critical attribute on a role — it decides who
# may assume it — so the permission to change it is worth measuring rather than
# reasoning about. Both phases name a service principal that cannot actually
# assume anything useful here.
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
#   make derive TYPE=aws_iam_role OPERATION=update #     FIXTURE=./derivefixtures/aws_iam_role__update_assume_policy

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
  name = "preflight-derive-role-trust"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = var.phase == 1 ? "ec2.amazonaws.com" : "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = {
    Name = "preflight-derive-role-trust"
    env  = "derive"
  }
}
