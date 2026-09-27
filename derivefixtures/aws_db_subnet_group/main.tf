# MAXIMAL create fixture for aws_db_subnet_group.
#
# NEEDS THE VPC SUPPORT FIXTURE, for its two subnets:
#
#   make derive TYPE=aws_db_subnet_group FIXTURE=./derivefixtures/aws_db_subnet_group #     SUPPORT=./derivefixtures/support/vpc
#
# THE CHEAP CORNER OF RDS. A DB subnet group is free and instant, unlike
# aws_db_instance which takes ~15 minutes per apply and bills real money.
#
# A SPECIFIC PREDICTION THIS RUN SHOULD CONFIRM OR REFUTE: the entry's read set is
# only rds:DescribeDBSubnetGroups, with no rds:ListTagsForResource. On 2026-09-26
# aws_db_parameter_group was measured and DID need it, because RDS does not return
# tags from its Describe calls the way ECS does. If that is a property of the service
# rather than of one resource, this entry is missing the same action.
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

variable "support_subnet_a_id" {
  description = "Subnet id exported by derivefixtures/support/vpc."
  type        = string
}

variable "support_subnet_b_id" {
  description = "Second subnet, in a different AZ. RDS requires a DB subnet group to span at least two zones."
  type        = string
}

resource "aws_db_subnet_group" "probe" {
  name        = "preflight-derive-dsg"
  description = "preflight mapping derivation; safe to delete"
  subnet_ids  = [var.support_subnet_a_id, var.support_subnet_b_id]

  tags = {
    Name = "preflight-derive-dsg"
    env  = "derive"
  }
}
