# Fixture for deriving aws_iam_instance_profile's required IAM actions.
#
# The resource it acts on is NOT created here. It is a support resource created
# out of band and referenced by name:
#
#   iam role preflight-derive-support
#
# That is a correctness requirement, not convenience. A fixture that created its
# own support resource would need that resource's actions too, and the
# derivation would attribute them to aws_iam_instance_profile — which
# would be wrong, because a derived set only means something if the fixture
# exercises exactly one resource type.
#
# Run: make derive TYPE=aws_iam_instance_profile FIXTURE=./derivefixtures/aws_iam_instance_profile

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {}

resource "aws_iam_instance_profile" "probe" {
  name = "preflight-derive-profile"
  role = "preflight-derive-support"
}
