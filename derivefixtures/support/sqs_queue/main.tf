# SUPPORT fixture: an SQS queue for aws_sqs_queue_policy to attach a policy to.
#
# NOT MEASURED. The harness applies this with OPERATOR credentials once before the
# loop and destroys it once after; the scratch role never touches it. See
# derivefixtures/README.md.
#
# Why it is not simply part of the measured fixture: the apply would then also need
# sqs:CreateQueue, sqs:GetQueueAttributes, sqs:TagQueue and the rest, the derivation
# would discover them, and they would land in aws_sqs_queue_policy's action list
# although they belong to aws_sqs_queue. A derived set only means anything if the
# fixture exercises exactly one resource type.
#
# The name is fixed and shared with the measured fixture. Passing Terraform outputs
# between two separate workspaces would need remote state or a wrapper; a documented
# constant is simpler and a maintainer tool can afford it.

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

resource "aws_sqs_queue" "support" {
  # Fixed, not name_prefix: the measured fixture has to be able to name it.
  # Recreating it inside SQS's 60-second cooldown is not a problem here, because
  # this stack is created once per run rather than once per attempt.
  name = "preflight-derive-support-queue"
}
