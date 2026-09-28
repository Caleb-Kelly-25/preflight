# MAXIMAL create fixture for aws_ecs_task_definition.
#
# NEEDS THE SERVICE-ROLE SUPPORT FIXTURE:
#
#   make derive TYPE=aws_ecs_task_definition FIXTURE=./derivefixtures/aws_ecs_task_definition #     SUPPORT=./derivefixtures/support/service_role
#
# COSTS NOTHING. A task definition is metadata — registering one starts no container
# and reserves no capacity. Nothing here is ever run by ECS.
#
# WHAT MAKES THIS ENTRY WORTH MEASURING is that it declares TWO iam:PassRole
# references against DIFFERENT attributes: execution_role_arn (the role ECS itself
# assumes to pull images and write logs) and task_role_arn (the role the container's
# own code assumes). Both are set here, to the same support role, so both references
# are exercised. aws_iam_instance_profile showed that two references against one
# attribute can need different operation sets; this is the other shape — two
# attributes feeding the same action.
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


variable "support_service_role_arn" {
  description = "Role ARN exported by derivefixtures/support/service_role."
  type        = string
}

resource "aws_ecs_task_definition" "probe" {
  family = "preflight-derive-taskdef"

  # Both role attributes, so both iam:PassRole references are exercised.
  execution_role_arn = var.support_service_role_arn
  task_role_arn      = var.support_service_role_arn

  # The smallest valid Fargate-compatible definition. cpu and memory are required
  # for Fargate and are pure metadata here — no capacity is reserved until a task
  # actually runs, and none ever does.
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([{
    name      = "probe"
    image     = "public.ecr.aws/docker/library/busybox:latest"
    essential = true
  }])

  tags = {
    Name = "preflight-derive-taskdef"
    env  = "derive"
  }
}
