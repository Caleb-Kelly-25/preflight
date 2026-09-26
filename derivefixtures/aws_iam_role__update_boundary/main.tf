# Two-phase fixture: aws_iam_role UPDATE, varying permissions_boundary.
#
# THIS ONE CLOSES A FALSE PASS. The entry lists nothing for a permissions-boundary
# change, and its own notes admit it: "a permissions boundary would need
# iam:PutRolePermissionsBoundary, which is absent here." So a plan that attaches a
# boundary to a role was reported as needing no permission for it — a silent pass
# for an apply that cannot succeed. Attaching a boundary is also exactly the kind
# of change a security-minded team makes, so the gap sat on a common path.
#
# THE BOUNDARY IS AN AWS-MANAGED POLICY ARN, on purpose. A customer-managed policy
# would be a second resource in the configuration, which Terraform would then
# manage — and the scratch role would need policy permissions that have nothing to
# do with the measurement, polluting the derived set. An AWS-managed ARN is a
# constant: nothing to create, nothing to read back, nothing to attribute.
#
# ReadOnlyAccess is used only as an arbitrary well-known ARN. Nothing here assumes
# the role or relies on what the boundary permits; a boundary only ever narrows.
#
# Phase 1 has no boundary, phase 2 attaches one. Everything else is constant.
#
# Run:
#   make derive TYPE=aws_iam_role OPERATION=update \
#     FIXTURE=./derivefixtures/aws_iam_role__update_boundary

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
  name = "preflight-derive-role-boundary"

  # The only thing that differs between phases. null means no boundary at all,
  # which is what makes phase 2 an ADD rather than a change between two
  # boundaries.
  permissions_boundary = var.phase == 1 ? null : "arn:aws:iam::aws:policy/ReadOnlyAccess"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = {
    Name = "preflight-derive-role-boundary"
    env  = "derive"
  }
}
