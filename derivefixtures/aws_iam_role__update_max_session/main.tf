# Two-phase fixture: aws_iam_role UPDATE, varying max_session_duration only.
#
# The other half of the iam:UpdateRole question. UpdateRole is the API that
# covers max_session_duration, and there is no separate call for it, so if this
# run does not need iam:UpdateRole then the action is unreachable and the entry
# is wrong about why it holds it.
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
#   make derive TYPE=aws_iam_role OPERATION=update #     FIXTURE=./derivefixtures/aws_iam_role__update_max_session

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
  name                 = "preflight-derive-role-maxsession"
  max_session_duration = var.phase == 1 ? 3600 : 7200
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = {
    Name = "preflight-derive-role-maxsession"
    env  = "derive"
  }
}
