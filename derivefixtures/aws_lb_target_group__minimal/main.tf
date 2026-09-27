# MINIMAL variant of aws_lb_target_group. Nothing optional set.
#
# NEEDS THE VPC SUPPORT FIXTURE:
#
#   make derive TYPE=aws_lb_target_group FIXTURE=./derivefixtures/aws_lb_target_group__minimal #     SUPPORT=./derivefixtures/support/vpc
#
# THE AFFORDABLE PART OF THE ELB FAMILY. A target group is free and instant; aws_lb
# and aws_lb_listener bill by the hour and need two subnets plus a live load
# balancer. So this entry is worth measuring even if those two are not.
#
# No tags, no health-check block, no stickiness. port, protocol and vpc_id are
# required by the provider and are not optional attributes.
#
# TWO CLAIMS THIS RUN SHOULD TEST. The entry declares
# elasticloadbalancing:ModifyTargetGroupAttributes UNGATED on create, reasoning that
# deregistration_delay and the stickiness settings are attributes rather than
# CreateTargetGroup parameters and so need a second call. If the minimal fixture does
# not need it, that reasoning is wrong in the over-reporting direction. And
# DescribeTags is in the read set, which has been a surplus guess before in EC2.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags: it applies to every resource in the configuration,
  # so keeping it here would silently tag this one and measure nothing.
}

variable "support_vpc_id" {
  description = "VPC id exported by derivefixtures/support/vpc."
  type        = string
}

resource "aws_lb_target_group" "probe" {
  name     = "preflight-derive-tg-min"
  port     = 80
  protocol = "HTTP"
  vpc_id   = var.support_vpc_id
}
