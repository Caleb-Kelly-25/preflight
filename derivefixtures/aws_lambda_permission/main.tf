# Fixture for deriving aws_lambda_permission.
#
#   make derive TYPE=aws_lambda_permission FIXTURE=./derivefixtures/aws_lambda_permission #     SUPPORT=./derivefixtures/support/lambda_function
#
# WHAT MAKES THIS ENTRY DIFFERENT FROM aws_lambda_function. It manipulates the
# function's RESOURCE-BASED POLICY directly, and it always acts on a function that
# already exists — so unlike the function itself, its `create` is
# resource_policy_capable too. `iam:SimulatePrincipalPolicy` cannot evaluate a
# resource-based policy for a role, so findings here carry that caveat permanently.
#
# The statement granted is inert: it lets the SNS service invoke a function that does
# nothing, from a source ARN in this account. Nothing subscribes it to anything.
#
# No gates on this entry, so no minimal variant is needed.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # No default_tags: a permission statement takes no tags.
}

variable "support_function_name" {
  description = "Function name exported by derivefixtures/support/lambda_function."
  type        = string
}

resource "aws_lambda_permission" "probe" {
  statement_id  = "preflight-derive-allow"
  action        = "lambda:InvokeFunction"
  function_name = var.support_function_name
  principal     = "sns.amazonaws.com"
}
