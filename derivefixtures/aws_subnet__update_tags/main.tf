# Two-phase fixture: aws_subnet UPDATE, varying TAGS only.
#
#   make derive TYPE=aws_subnet OPERATION=update \
#     FIXTURE=./derivefixtures/aws_subnet__update_tags \
#     SUPPORT=./derivefixtures/support/vpc
#
# ITS PURPOSE IS TO LET ec2:DescribeTags BE REMOVED SAFELY. All three create variants
# reported it surplus, and it was already proven surplus on aws_vpc and
# aws_security_group, which both dropped it — aws_subnet is the last entry carrying
# it. But read_actions union into create, update AND delete, and the ECS case showed
# what happens when you narrow a read set on partial evidence:
# ecs:ListTagsForResource was surplus on create and delete and REQUIRED on update, so
# removing it would have been a false pass.
#
# An update that changes tags is the operation most likely to need a tag read, which
# makes this the decisive case rather than a formality.
#
# Phase 2 both CHANGES a tag and REMOVES one, so both ec2:CreateTags and
# ec2:DeleteTags are exercised in one apply.

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

variable "support_vpc_id" {
  description = "VPC id exported by derivefixtures/support/vpc."
  type        = string
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

resource "aws_subnet" "probe" {
  vpc_id     = var.support_vpc_id
  cidr_block = "10.42.103.0/24"

  # The only thing that differs between phases: `env` changes and `doomed` is dropped.
  tags = var.phase == 1 ? {
    Name   = "preflight-derive-subnet-upd"
    env    = "derive"
    doomed = "removed-in-phase-2"
    } : {
    Name = "preflight-derive-subnet-upd"
    env  = "derive-changed"
  }
}
