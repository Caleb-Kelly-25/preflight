# Fixture for deriving aws_iam_role_policy_attachment's required IAM actions.
#
# The resource it acts on is NOT created here. It is a support resource created
# out of band and referenced by name:
#
#   iam role + managed policy preflight-derive-support*
#
# That is a correctness requirement, not convenience. A fixture that created its
# own support resource would need that resource's actions too, and the
# derivation would attribute them to aws_iam_role_policy_attachment — which
# would be wrong, because a derived set only means something if the fixture
# exercises exactly one resource type.
#
# Run: make derive TYPE=aws_iam_role_policy_attachment FIXTURE=./derivefixtures/aws_iam_role_policy_attachment

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

variable "account_id" {
  description = "Account the fixture runs in. Supplied by the harness as TF_VAR_account_id, so no account number is ever written into this repository."
  type        = string
}

provider "aws" {}

resource "aws_iam_role_policy_attachment" "probe" {
  role       = "preflight-derive-support"
  policy_arn = "arn:aws:iam::${var.account_id}:policy/preflight-derive-support-policy"
}
