# MINIMAL variant of aws_lambda_function. Nothing optional set.
#
# NEEDS THE SERVICE-ROLE SUPPORT FIXTURE:
#
#   make derive TYPE=aws_lambda_function FIXTURE=./derivefixtures/aws_lambda_function__minimal #     SUPPORT=./derivefixtures/support/service_role
#
# COSTS NOTHING. The function is created and destroyed and never invoked, so no
# request or duration charges arise; code storage this small is inside the permanent
# free allowance.
#
# THE ZIP IS BUILT IN-PROCESS by hashicorp/archive from inline content, so no binary
# artifact is committed and nothing has to exist on disk. archive_file is a LOCAL
# data source — it reads no AWS API — which matters, because a data source in a
# measured fixture is evaluated under the SCRATCH role and any AWS permission it
# needed would be attributed to this resource type.
#
# THE HANDLER IS NEVER CALLED and deliberately does nothing.
#
# No tags, no reserved concurrency. Establishes the bare create path: the function
# itself, its read-backs, and iam:PassRole against the role it hands to Lambda.

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
  # Deliberately no default_tags: it applies to every resource in the configuration,
  # so keeping it would silently tag the function and measure nothing.
}


variable "support_service_role_arn" {
  description = "Role ARN exported by derivefixtures/support/service_role."
  type        = string
}

data "archive_file" "code" {
  type        = "zip"
  output_path = "${path.module}/function.zip"

  source {
    filename = "index.js"
    content  = "exports.handler = async () => ({ statusCode: 200 });"
  }
}

resource "aws_lambda_function" "probe" {
  function_name = "preflight-derive-fn-min"
  role          = var.support_service_role_arn

  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
  handler          = "index.handler"
  runtime          = "nodejs20.x"
}
