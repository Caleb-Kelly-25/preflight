# Fixture testing whether a `default_tags`-only change requires the tagging
# permission — and therefore whether every `attribute_changed: [tags]` gate in
# the database is a false pass.
#
# THE QUESTION. Provider-level `default_tags` merge into each resource's
# computed `tags_all`, not into its `tags`. So when a user changes only
# `default_tags`, the plan shows `tags_all` changed and `tags` UNCHANGED. Every
# tags gate in the database is written `attribute_changed: [tags]`, which would
# then be false, the tagging action would be dropped, and the engine would
# report a plan as safe that cannot apply.
#
# This is not a question about aws_iam_policy. It is a question about the gate
# pattern, and IAM is simply the cheapest place to ask it.
#
# THE SHAPE. The resource's own `tags` are byte-identical across both phases, as
# is the policy document. The ONLY thing that differs is the provider's
# `default_tags` block. If phase 2 needs iam:TagPolicy, the gate pattern is
# wrong wherever it appears.
#
# Run via:
#   make derive TYPE=aws_iam_policy OPERATION=update \
#     FIXTURE=./derivefixtures/aws_iam_policy__update_default_tags

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

# The variable under test. Phase 2 adds a second default tag; nothing else in
# this file changes.
provider "aws" {
  default_tags {
    tags = var.phase == 1 ? {
      preflight-derive = "true"
      } : {
      preflight-derive = "true"
      added-in-phase-2 = "true"
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
  name        = "preflight-derive-policy-default-tags"
  description = "preflight mapping derivation; safe to delete"

  # Constant across phases.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = "s3:GetObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })

  # Constant across phases. Only `default_tags` above varies, so a denial here
  # can only come from the merged tag set.
  tags = {
    Name = "preflight-derive-policy-default-tags"
    env  = "derive"
  }
}
