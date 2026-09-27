# Fixture for deriving aws_sqs_queue_policy.
#
# NEEDS A SUPPORT FIXTURE — the queue is not created here:
#
#   make derive TYPE=aws_sqs_queue_policy \
#     FIXTURE=./derivefixtures/aws_sqs_queue_policy \
#     SUPPORT=./derivefixtures/support/sqs_queue
#
# Creating the queue here would pull sqs:CreateQueue and its read-backs into the
# measurement and attribute them to this resource type. See the support fixture.
#
# WHAT MAKES THIS ENTRY INTERESTING is that its create acts on a queue that ALREADY
# EXISTS, so unlike aws_sqs_queue the queue's own resource policy can deny it — the
# entry declares resource_policy_capable for all three operations. The policy
# written here is permissive to the account itself so the run cannot lock itself
# out; a deny-all policy would make the harness's own teardown fail.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # No default_tags: a queue policy takes no tags.
}

variable "account_id" {
  description = "Set by the harness from the verified caller identity."
  type        = string
}

data "aws_region" "current" {}

resource "aws_sqs_queue_policy" "probe" {
  queue_url = "https://sqs.${data.aws_region.current.region}.amazonaws.com/${var.account_id}/preflight-derive-support-queue"

  # Deliberately permissive to this account only, and to nobody else. A restrictive
  # policy here would risk denying the operator teardown that has to run afterwards.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = var.account_id }
      Action    = "sqs:GetQueueAttributes"
      Resource  = "arn:aws:sqs:${data.aws_region.current.region}:${var.account_id}:preflight-derive-support-queue"
    }]
  })
}
