# SUPPORT fixture: a Lambda function, its role, and an SQS queue to trigger it.
#
# NOT MEASURED. Applied once with OPERATOR credentials and destroyed after the run.
#
# Serves two measured fixtures:
#
#   aws_lambda_permission             needs a function to attach a policy statement to
#   aws_lambda_event_source_mapping   needs a function AND an event source
#
# Creating the function inside either measured fixture would pull lambda:CreateFunction,
# iam:PassRole and four read-backs into the measurement and attribute them to the
# wrong resource type — aws_lambda_function's own create needs seven actions.
#
# COSTS NOTHING. The function is never invoked, and the queue is never written to.
#
# THE ROLE CARRIES AN SQS POLICY, unlike the other support roles which grant nothing.
# AWS validates at CreateEventSourceMapping that the function's execution role can
# actually read the source queue, and refuses the mapping otherwise — so a
# permission-less role would fail the measured apply for a reason that has nothing to
# do with the caller's own permissions. It is scoped to this one throwaway queue.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
    archive = {
      source = "hashicorp/archive"
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
  name = "preflight-derive-support-esm-queue"
}

resource "aws_iam_role" "support" {
  name = "preflight-derive-support-fn-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "support_sqs" {
  name = "preflight-derive-support-sqs"
  role = aws_iam_role.support.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:GetQueueAttributes",
      ]
      Resource = aws_sqs_queue.support.arn
    }]
  })
}

data "archive_file" "code" {
  type        = "zip"
  output_path = "${path.module}/function.zip"

  source {
    filename = "index.js"
    content  = "exports.handler = async () => ({ statusCode: 200 });"
  }
}

resource "aws_lambda_function" "support" {
  function_name = "preflight-derive-support-fn"
  role          = aws_iam_role.support.arn

  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
  handler          = "index.handler"
  runtime          = "nodejs20.x"

  # The inline policy must exist before the function, or an event source mapping
  # created immediately afterwards can be rejected for a role that cannot yet read
  # the queue.
  depends_on = [aws_iam_role_policy.support_sqs]
}

output "support_function_name" {
  value = aws_lambda_function.support.function_name
}

output "support_function_arn" {
  value = aws_lambda_function.support.arn
}

output "support_queue_arn" {
  value = aws_sqs_queue.support.arn
}
