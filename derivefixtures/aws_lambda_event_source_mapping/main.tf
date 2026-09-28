# MAXIMAL create fixture for aws_lambda_event_source_mapping.
#
#   make derive TYPE=aws_lambda_event_source_mapping #     FIXTURE=./derivefixtures/aws_lambda_event_source_mapping #     SUPPORT=./derivefixtures/support/lambda_function
#
# DISABLED ON PURPOSE. An enabled mapping polls the queue continuously, which costs
# SQS requests for as long as the fixture stands and would keep running through every
# attempt of the loop. `enabled = false` creates the mapping, exercises exactly the
# same permissions, and polls nothing.
#
# The support role carries an SQS read policy for a reason worth knowing: AWS
# validates at CreateEventSourceMapping that the FUNCTION's execution role can read
# the source queue, and refuses otherwise. That is a check on the function's role, not
# on the caller, so it would have failed this measurement for a reason unrelated to
# the permission being measured.
#
# Pairs with __minimal to measure the tag gate.

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

  tags = {
    Name = "preflight-derive-esm"
    env  = "derive"
  }
}
