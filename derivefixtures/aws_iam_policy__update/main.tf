# Fixture for deriving aws_iam_policy's UPDATE path.
#
# Two-phase. `phase = 1` is the before state, applied by the harness with
# OPERATOR credentials; `phase = 2` is the change being measured, applied under
# the scratch role. See derivefixtures/README.md for why the split exists and
# what breaks without it.
#
# What changes between the phases is the policy DOCUMENT and nothing else. That
# is deliberate:
#
#   - Tags are identical in both phases, so iam:TagPolicy and iam:UntagPolicy
#     stay out of the measurement. A tag change is a different update path and
#     mixing the two would produce one set with no way to say which attribute
#     required what.
#   - `description` is identical because AWS cannot update it on a managed
#     policy at all. Changing it here would force a replace, and a replace is a
#     create plus a delete wearing an update's name.
#   - `name` is identical for the same reason: changing it replaces the policy.
#
# The claim being tested is that an update needs iam:CreatePolicyVersion and
# iam:DeletePolicyVersion. The second is reasoned from the five-version limit —
# the provider prunes the oldest version when it hits it — and going from one
# version to two does not reach that limit, so this run is expected to report it
# surplus. Surplus over-reports, which fails safe, but the entry should say which
# it is rather than implying both were measured.
#
# Run via:
#   make derive TYPE=aws_iam_policy OPERATION=update \
#     FIXTURE=./derivefixtures/aws_iam_policy__update

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
  name = "preflight-derive-policy-update"
  # Constant across phases on purpose: AWS cannot update a managed policy's
  # description, so changing it would replace the resource.
  description = "preflight mapping derivation; safe to delete"

  # The only thing that differs between phases. Both documents are inert: they
  # DENY an action on a bucket that does not exist, so the policy grants nothing
  # whether or not anything ever attaches it.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = var.phase == 1 ? "s3:GetObject" : "s3:PutObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })

  # Identical in both phases, so the tag gate is not part of this measurement.
  tags = {
    Name = "preflight-derive-policy-update"
    env  = "derive"
  }
}
