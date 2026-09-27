# SUPPORT fixture: an SNS topic, plus an SQS queue to subscribe to it.
#
# NOT MEASURED. Applied once with OPERATOR credentials before the loop and destroyed
# after; the scratch role never touches it. See derivefixtures/README.md.
#
# Serves two measured fixtures, which is why both resources live here rather than in
# two support stacks:
#
#   aws_sns_topic_policy        needs the topic
#   aws_sns_topic_subscription  needs the topic AND an endpoint to subscribe
#
# An SQS queue is the endpoint because it is deterministic. The other protocols are
# not usable in a derivation: email sends a real confirmation message to a real
# address, and http/https leaves the subscription pending on an endpoint that never
# confirms.

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

resource "aws_sns_topic" "support" {
  name = "preflight-derive-support-topic"
}

resource "aws_sqs_queue" "support" {
  # Fixed name so the measured fixture can build its ARN. Recreating inside SQS's
  # 60-second cooldown is not a risk here: this stack is applied once per RUN, not
  # once per attempt.
  name = "preflight-derive-support-sub-queue"
}
