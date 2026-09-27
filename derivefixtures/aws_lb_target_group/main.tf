# MAXIMAL create fixture for aws_lb_target_group.
#
# NEEDS THE VPC SUPPORT FIXTURE:
#
#   make derive TYPE=aws_lb_target_group FIXTURE=./derivefixtures/aws_lb_target_group #     SUPPORT=./derivefixtures/support/vpc
#
# THE AFFORDABLE PART OF THE ELB FAMILY. A target group is free and instant; aws_lb
# and aws_lb_listener bill by the hour and need two subnets plus a live load
# balancer. So this entry is worth measuring even if those two are not.
#
# Pairs with __minimal to measure the tag gate. This entry has the most gated
# attributes of any in the database (seven), so full attribution would need far more
# fixtures; this pair settles the tag gate and the create path.
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
  default_tags {
    tags = {
      preflight-derive = "true"
    }
  }
}

variable "support_vpc_id" {
  description = "VPC id exported by derivefixtures/support/vpc."
  type        = string
}

resource "aws_lb_target_group" "probe" {
  name     = "preflight-derive-tg"
  port     = 80
  protocol = "HTTP"
  vpc_id   = var.support_vpc_id

  tags = {
    Name = "preflight-derive-tg"
    env  = "derive"
  }
}
