# MAXIMAL create fixture for aws_iam_role.
#
# Its counterpart is derivefixtures/aws_iam_role__minimal, and the pair is how a
# `when` gate gets measured: derive both, and the difference between the two
# derived sets IS the gate. The minimal fixture has referenced this one in its
# header since it was written, but this directory did not exist — the entry's
# create path came from a hand-run before the harness did, so there was nothing
# to commit. That is now fixed.
#
# MAXIMAL MEANS EVERY OPTIONAL ATTRIBUTE IS SET, which is the point: an attribute
# a fixture never sets is an action nobody measured, and the entry then claims
# completeness it has not established. `permissions_boundary` is here for exactly
# that reason — a role created WITH a boundary may or may not need
# iam:PutRolePermissionsBoundary on top of iam:CreateRole, because CreateRole
# accepts a PermissionsBoundary parameter and IAM may authorise it either way.
# That has gone three different ways for tagging across three services, so it is
# not inferable and has to be run.
#
# The boundary is an AWS-managed policy ARN so that nothing else enters the
# configuration: a customer-managed policy would be a second resource Terraform
# manages, and the scratch role would need policy permissions that have nothing
# to do with this measurement. ReadOnlyAccess is used only as a well-known
# constant; nothing here assumes the role, and a boundary only ever narrows.
#
# Run: make derive TYPE=aws_iam_role FIXTURE=./derivefixtures/aws_iam_role

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

resource "aws_iam_role" "probe" {
  name                 = "preflight-derive-role-max"
  description          = "preflight mapping derivation; safe to delete"
  max_session_duration = 7200
  permissions_boundary = "arn:aws:iam::aws:policy/ReadOnlyAccess"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })

  tags = {
    Name = "preflight-derive-role-max"
    env  = "derive"
  }
}
