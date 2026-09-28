# SUPPORT fixture: an IAM role that Lambda and ECS tasks can assume.
#
# NOT MEASURED. Applied once with OPERATOR credentials and destroyed after the run.
#
# Serves the whole Lambda and ECS family, every member of which hands a role to a
# service and therefore needs iam:PassRole against it:
#
#   aws_lambda_function        role
#   aws_ecs_task_definition    execution_role_arn and task_role_arn
#   aws_lambda_permission      (indirectly, via the function)
#
# ONE ROLE TRUSTING TWO SERVICES rather than two roles. The trust policy is the only
# thing that differs between a Lambda role and an ECS task role, and a role may name
# several service principals, so one covers both. Fewer support resources means fewer
# things a failed teardown can leak.
#
# THE ROLE GRANTS NOTHING. It has no policies attached at all — a role that can be
# assumed and permits nothing. That is enough: iam:PassRole is authorised against the
# role's ARN and does not care what the role can do, and a derivation fixture must
# never be able to hold privilege.
#
# The ARN is exported because iam:PassRole is checked against it, and a role ARN is
# predictable from the name — but exporting it keeps the measured fixtures free of
# hardcoded account ids.

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

resource "aws_iam_role" "support" {
  name = "preflight-derive-support-service"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = ["lambda.amazonaws.com", "ecs-tasks.amazonaws.com"]
      }
      Action = "sts:AssumeRole"
    }]
  })
}

output "support_service_role_arn" {
  value = aws_iam_role.support.arn
}
