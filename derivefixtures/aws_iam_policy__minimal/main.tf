# MINIMAL variant of the aws_iam_policy fixture, existing to MEASURE A `when` GATE.
#
# `when` is the only schema feature that can introduce a false pass, because it
# removes actions from a requirement list. A gate is only reasoning until the
# negative case has been run: deriving the same type from a maximal fixture and
# from this one, the difference between the two derived sets IS the gate.
#
#   derivefixtures/aws_iam_policy                    maximal  -> gated action required
#   derivefixtures/aws_iam_policy__minimal           -> should NOT be
#
# NOTE THE ABSENT default_tags. The maximal fixtures set one so leaked resources
# can be swept by tag, but default_tags applies to EVERY resource in the
# configuration — a minimal fixture that keeps it silently tags the resource and
# measures nothing. That mistake was nearly shipped once.
#
# Run: make derive TYPE=aws_iam_policy FIXTURE=./derivefixtures/aws_iam_policy__minimal

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags. See above.
}

resource "aws_iam_policy" "probe" {
  name = "preflight-derive-policy-min"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Deny"
      Action   = "s3:GetObject"
      Resource = "arn:aws:s3:::preflight-derive-nonexistent/*"
    }]
  })
}
