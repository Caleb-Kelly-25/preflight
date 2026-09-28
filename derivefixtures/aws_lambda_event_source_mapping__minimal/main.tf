# MINIMAL variant of aws_lambda_event_source_mapping, to measure the tag gate.
#
# If an untagged mapping still requires lambda:TagResource the gate is wrong and the
# entry under-reports for everyone who does not tag.
#
# NOTE THE ABSENT default_tags: it applies to every resource in the configuration, so
# keeping it would silently tag the mapping and measure nothing.

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

variable "support_function_arn" {
  description = "Function ARN exported by derivefixtures/support/lambda_function."
  type        = string
}

variable "support_queue_arn" {
  description = "Queue ARN exported by derivefixtures/support/lambda_function."
  type        = string
}

resource "aws_lambda_event_source_mapping" "probe" {
  event_source_arn = var.support_queue_arn
  function_name    = var.support_function_arn
  enabled          = false
}
