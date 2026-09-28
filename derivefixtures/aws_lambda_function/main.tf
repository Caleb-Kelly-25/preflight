# MAXIMAL create fixture for aws_lambda_function.
#
# NEEDS THE SERVICE-ROLE SUPPORT FIXTURE:
#
#   make derive TYPE=aws_lambda_function FIXTURE=./derivefixtures/aws_lambda_function #     SUPPORT=./derivefixtures/support/service_role
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
# TWO GATES EXERCISED HERE — tags and reserved_concurrent_executions — so this pairs
# with __minimal and __tagsonly to attribute them separately. A maximal/minimal pair
# alone would only show both actions becoming unnecessary together.
#
# runtime_management_config carries a third gate and is NOT set: it selects how AWS
# rolls out runtime updates, which is orthogonal to permissions and would need a
# fourth fixture to attribute. That gate stays reasoning.

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
  function_name = "preflight-derive-fn"
  role          = var.support_service_role_arn

  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
  handler          = "index.handler"
  runtime          = "nodejs20.x"

  # The attribute the lambda:PutFunctionConcurrency gate keys on. Zero means "this
  # function may not run at all", which is the safest possible value for a fixture
  # and still exercises the gate — reserving capacity is what needs the permission,
  # not the amount.
  reserved_concurrent_executions = 0

  tags = {
    Name = "preflight-derive-fn"
    env  = "derive"
  }
}
