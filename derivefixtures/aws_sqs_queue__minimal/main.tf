# MINIMAL variant of the aws_sqs_queue fixture, existing to MEASURE THE TAG GATE.
#
# `when` is the only schema feature that can introduce a false pass, because it
# REMOVES actions. If an untagged queue still requires sqs:TagQueue, the gate is
# wrong and the entry under-reports for everyone who does not tag.
#
# NOTE THE ABSENT default_tags: it applies to every resource in the configuration,
# so a minimal fixture that kept one would silently tag the queue and measure
# nothing.
#
# name_prefix for the same reason as the maximal fixture — SQS will not reuse a
# deleted queue's name for sixty seconds.
#
# Run: make derive TYPE=aws_sqs_queue FIXTURE=./derivefixtures/aws_sqs_queue__minimal

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

# No tags. Nothing optional. That is the whole point.
resource "aws_sqs_queue" "probe" {
  name_prefix = "preflight-derive-qmin-"
}
