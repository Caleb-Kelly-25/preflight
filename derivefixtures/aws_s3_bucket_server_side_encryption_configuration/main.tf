# Fixture for deriving aws_s3_bucket_server_side_encryption_configuration's required IAM actions.
#
# The resource it acts on is NOT created here. It is a support resource created
# out of band and referenced by name:
#
#   s3 bucket preflight-derive-support-000000000000
#
# That is a correctness requirement, not convenience. A fixture that created its
# own support resource would need that resource's actions too, and the
# derivation would attribute them to aws_s3_bucket_server_side_encryption_configuration — which
# would be wrong, because a derived set only means something if the fixture
# exercises exactly one resource type.
#
# Run: make derive TYPE=aws_s3_bucket_server_side_encryption_configuration FIXTURE=./derivefixtures/aws_s3_bucket_server_side_encryption_configuration

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {}

resource "aws_s3_bucket_server_side_encryption_configuration" "probe" {
  bucket = "preflight-derive-support-000000000000"
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}
