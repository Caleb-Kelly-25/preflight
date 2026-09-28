# Two-phase fixture: aws_lambda_function UPDATE, varying TAGS only.
#
#   make derive TYPE=aws_lambda_function OPERATION=update \
#     FIXTURE=./derivefixtures/aws_lambda_function__update_tags \
#     SUPPORT=./derivefixtures/support/service_role
#
# ITS PURPOSE IS TO LET TWO READ ACTIONS BE REMOVED SAFELY. lambda:ListTags and
# lambda:GetRuntimeManagementConfig came back SURPLUS on create (all three variants)
# and on delete — Lambda returns tags inline from GetFunction, as ECS does from
# DescribeClusters and unlike RDS.
#
# Two measurements out of three are not enough. The ECS case settled that:
# ecs:ListTagsForResource was surplus on create and delete and REQUIRED on update, so
# removing it on partial evidence would have been a false pass. An update that
# changes tags is precisely the operation most likely to read tags back, which makes
# this the deciding run rather than a formality.
#
# Phase 2 both CHANGES a tag and REMOVES one, so both tagging directions are covered.
# Everything else — the code, the handler, the runtime, the role — is identical
# across phases, so nothing but the tag change is measured.

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

# 1 = the before state, established with operator credentials.
# 2 = the change under measurement, applied by the scratch role.
variable "phase" {
  type    = number
  default = 1

  validation {
    condition     = contains([1, 2], var.phase)
    error_message = "phase must be 1 (before) or 2 (the measured change)."
  }
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
  function_name = "preflight-derive-fn-upd"
  role          = var.support_service_role_arn

  filename         = data.archive_file.code.output_path
  source_code_hash = data.archive_file.code.output_base64sha256
  handler          = "index.handler"
  runtime          = "nodejs20.x"

  # The only thing that differs between phases.
  tags = var.phase == 1 ? {
    Name   = "preflight-derive-fn-upd"
    env    = "derive"
    doomed = "removed-in-phase-2"
    } : {
    Name = "preflight-derive-fn-upd"
    env  = "derive-changed"
  }
}
