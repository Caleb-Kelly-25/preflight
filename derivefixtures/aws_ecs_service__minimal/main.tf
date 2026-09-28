# MINIMAL variant of aws_ecs_service, to measure the tag gate.
#
#   make derive TYPE=aws_ecs_service FIXTURE=./derivefixtures/aws_ecs_service__minimal #     SUPPORT=./derivefixtures/support/ecs_service
#
# DESIRED_COUNT = 0, which is what makes this free. The service exists, is created and
# deleted through exactly the same API calls as a real one, and never places a task.
# The support cluster has no registered instances either, so nothing could run even if
# the count were raised.
#
# THE ENTRY DECLARES iam:PassRole AGAINST `iam_role`, scoped to create only. That
# attribute is for the legacy ELB-integration service role and is NOT set here — a
# service without a load balancer does not hand a role to ECS — so this run should
# report that reference SURPLUS. That is the expected result, not a finding: the
# harness seeds every reference unconditionally because it has no plan to evaluate a
# `when` gate against.
#
# No tags. If an untagged service still needs ecs:TagResource the gate is wrong.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags: it would silently tag the service.
}


variable "support_cluster_name" {
  description = "Cluster name exported by derivefixtures/support/ecs_service."
  type        = string
}

variable "support_taskdef_arn" {
  description = "Task definition ARN exported by derivefixtures/support/ecs_service."
  type        = string
}

resource "aws_ecs_service" "probe" {
  name            = "preflight-derive-service-min"
  cluster         = var.support_cluster_name
  task_definition = var.support_taskdef_arn

  # Zero tasks: the whole point. Creating the service exercises ecs:CreateService and
  # its read-backs without placing anything.
  desired_count = 0

  # EC2 is the default launch type and needs no network_configuration with the
  # support task definition's bridge networking.
  launch_type = "EC2"
}
