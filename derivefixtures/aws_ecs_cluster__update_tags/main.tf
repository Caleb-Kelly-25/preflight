# Two-phase fixture: aws_ecs_cluster UPDATE, varying TAGS only.
#
# ITS PURPOSE IS TO LET AN ACTION BE REMOVED SAFELY. The create and delete
# derivations both reported ecs:ListTagsForResource SURPLUS, because
# ecs:DescribeClusters returns tags inline and no separate list call is ever made.
# But read_actions are unioned into create, update AND delete, so removing it on
# the strength of two measurements out of three would be exactly the reasoning that
# produces a false pass — an update is the operation most likely to read tags back.
#
# A tag change is also the only update this resource really has: `name` forces
# replacement, and the remaining attributes are nested `setting` and
# `configuration` blocks.
#
# Phase 2 both CHANGES a tag and REMOVES one, so both tagging directions are
# exercised in a single apply.
#
# Run:
#   make derive TYPE=aws_ecs_cluster OPERATION=update \
#     FIXTURE=./derivefixtures/aws_ecs_cluster__update_tags

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

resource "aws_ecs_cluster" "probe" {
  name = "preflight-derive-cluster-tags"

  # The only thing that differs between phases: `env` changes and `doomed` is
  # dropped, so both tagging directions are covered.
  tags = var.phase == 1 ? {
    Name   = "preflight-derive-cluster-tags"
    env    = "derive"
    doomed = "removed-in-phase-2"
    } : {
    Name = "preflight-derive-cluster-tags"
    env  = "derive-changed"
  }
}
