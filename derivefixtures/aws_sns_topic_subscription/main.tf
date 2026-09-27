# Fixture for deriving aws_sns_topic_subscription.
#
# NEEDS A SUPPORT FIXTURE — both the topic and the endpoint queue come from it:
#
#   make derive TYPE=aws_sns_topic_subscription #     FIXTURE=./derivefixtures/aws_sns_topic_subscription #     SUPPORT=./derivefixtures/support/sns_topic
#
# THE PROTOCOL IS sqs BECAUSE IT IS DETERMINISTIC. The alternatives cannot be used
# in a derivation: email sends a real confirmation message to a real address, and
# http/https leaves the subscription pending on an endpoint that never confirms — so
# neither reaches a steady state the loop can destroy and repeat.
#
# Subscribing an SQS queue to a topic needs sns:Subscribe from the CALLER; whether
# the queue accepts the messages is governed by the queue's own policy, which is not
# this caller's problem and not part of this measurement.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # No default_tags: none of these sub-resources take tags.
}

variable "account_id" {
  description = "Set by the harness from the verified caller identity."
  type        = string
}

data "aws_region" "current" {}

resource "aws_sns_topic_subscription" "probe" {
  topic_arn = "arn:aws:sns:${data.aws_region.current.region}:${var.account_id}:preflight-derive-support-topic"
  protocol  = "sqs"
  endpoint  = "arn:aws:sqs:${data.aws_region.current.region}:${var.account_id}:preflight-derive-support-sub-queue"
}
