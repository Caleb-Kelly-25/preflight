# SUPPORT fixture: an ECS cluster and a task definition for aws_ecs_service.
#
# NOT MEASURED. Applied once with OPERATOR credentials and destroyed after the run.
#
# Creating either inside the measured fixture would pull ecs:CreateCluster,
# ecs:RegisterTaskDefinition, iam:PassRole and their read-backs into the measurement
# and attribute them to aws_ecs_service, which needs none of them.
#
# COSTS NOTHING, and the shape is chosen to keep it that way. The task definition uses
# the EC2 launch type with `bridge` networking rather than Fargate, and the service
# below runs DESIRED_COUNT = 0. So:
#
#   - no Fargate task ever starts, so no per-second compute charge;
#   - `bridge` needs no network_configuration, so no subnets and no support VPC;
#   - the cluster has no capacity providers and no registered instances, so nothing
#     could run even if the count were raised.
#
# An empty ECS cluster and an unused task definition are both free.

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

resource "aws_ecs_cluster" "support" {
  name = "preflight-derive-support-cluster"
}

resource "aws_ecs_task_definition" "support" {
  family = "preflight-derive-support-taskdef"

  # EC2 launch type with bridge networking: no subnets required, nothing to bill.
  network_mode = "bridge"

  container_definitions = jsonencode([{
    name      = "probe"
    image     = "public.ecr.aws/docker/library/busybox:latest"
    essential = true
    memory    = 128
  }])
}

output "support_cluster_name" {
  value = aws_ecs_cluster.support.name
}

output "support_taskdef_arn" {
  value = aws_ecs_task_definition.support.arn
}
