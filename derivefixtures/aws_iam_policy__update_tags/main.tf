# Fixture for deriving aws_iam_policy's UPDATE path when only TAGS change.
#
# The sibling fixture aws_iam_policy__update measures a policy-DOCUMENT change.
# This one measures a tag change, and the two are separate because an update
# path depends on which attribute changed: the engine gates update actions with
# `attribute_changed`, so each branch of the gate needs its own measurement or
# the branch nobody measured is a false pass.
#
# That gap was real. Before this fixture existed, the entry's update path listed
# only the policy-version actions, so a plan that changed nothing but tags would
# have been reported as needing no tagging permission at all.
#
# Phase 2 both CHANGES a tag and REMOVES one, which is deliberate: IAM has
# separate actions for the two directions, and a fixture that only added tags
# would never exercise iam:UntagPolicy.
#
# The policy document is byte-identical across phases, so the version actions
# stay out of this measurement.
#
# Run via:
#   make derive TYPE=aws_iam_policy OPERATION=update #     FIXTURE=./derivefixtures/aws_iam_policy__update_tags

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

resource "aws_iam_policy" "probe" {
  name = "preflight-derive-policy-update-tags"
  # Constant across phases on purpose: AWS cannot update a managed policy's
  # description, so changing it would replace the resource.
  description = "preflight mapping derivation; safe to delete"

  # Identical in both phases: this fixture measures tags, not the document.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = "s3:GetObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })

  # The only thing that differs between phases. Phase 2 changes `env` and drops
  # `doomed` entirely, so both tagging directions are exercised in one apply.
  tags = var.phase == 1 ? {
    Name   = "preflight-derive-policy-update-tags"
    env    = "derive"
    doomed = "removed-in-phase-2"
    } : {
    Name = "preflight-derive-policy-update-tags"
    env  = "derive-changed"
  }
}
