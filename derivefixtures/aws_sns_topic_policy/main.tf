# Fixture for deriving aws_sns_topic_policy.
#
# NEEDS A SUPPORT FIXTURE — the topic is not created here:
#
#   make derive TYPE=aws_sns_topic_policy #     FIXTURE=./derivefixtures/aws_sns_topic_policy #     SUPPORT=./derivefixtures/support/sns_topic
#
# WHY THIS ENTRY DIFFERS FROM aws_sns_topic. Its create rewrites the policy of a
# topic that ALREADY EXISTS, so the topic's current policy governs that call — the
# entry declares resource_policy_capable for all three operations, where the topic
# itself is exempt on create. That distinction is the whole reason both entries
# exist.
#
# The policy written is permissive to this account only. A restrictive one risks
# denying the operator teardown that has to run afterwards, which would abort the
# run and leak the topic.

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

resource "aws_sns_topic_policy" "probe" {
  arn = "arn:aws:sns:${data.aws_region.current.region}:${var.account_id}:preflight-derive-support-topic"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = var.account_id }
      Action    = "SNS:GetTopicAttributes"
      Resource  = "arn:aws:sns:${data.aws_region.current.region}:${var.account_id}:preflight-derive-support-topic"
    }]
  })
}
