# SUPPORT fixture: a VPC and two subnets in different availability zones.
#
# NOT MEASURED. Applied once with OPERATOR credentials before the loop and destroyed
# after; the scratch role never applies it. See derivefixtures/README.md.
#
# Serves three measured fixtures, and is a prerequisite for the ELB family:
#
#   aws_subnet             needs the VPC (it creates its own subnet inside it)
#   aws_lb_target_group    needs the VPC
#   aws_db_subnet_group    needs TWO subnets in different AZs, which is an RDS rule
#
# Creating a VPC inside a measured fixture would pull ec2:CreateVpc,
# ec2:ModifyVpcAttribute, ec2:DescribeVpcs and ec2:DescribeVpcAttribute into the
# measurement and attribute them to whichever type was under test. aws_vpc's own
# create needs five actions, and every one would have landed in the wrong entry.
#
# THE AVAILABILITY ZONES ARE LOOKED UP HERE, in the support stack, on purpose. A
# `data "aws_availability_zones"` block in a MEASURED fixture would be read under the
# scratch role, so ec2:DescribeAvailabilityZones would be discovered by the loop and
# attributed to the resource type under test — which does not need it. Anything the
# measured fixture reads is part of the measurement.
#
# Everything here is free: a VPC and subnets cost nothing, and nothing is attached.

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

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "support" {
  cidr_block = "10.42.0.0/16"

  tags = {
    Name = "preflight-derive-support-vpc"
  }
}

# Two subnets, two AZs. RDS requires a DB subnet group to span at least two zones,
# which is the only reason there are two.
resource "aws_subnet" "support_a" {
  vpc_id            = aws_vpc.support.id
  cidr_block        = "10.42.1.0/24"
  availability_zone = data.aws_availability_zones.available.names[0]

  tags = {
    Name = "preflight-derive-support-subnet-a"
  }
}

resource "aws_subnet" "support_b" {
  vpc_id            = aws_vpc.support.id
  cidr_block        = "10.42.2.0/24"
  availability_zone = data.aws_availability_zones.available.names[1]

  tags = {
    Name = "preflight-derive-support-subnet-b"
  }
}

# Handed to the measured fixtures as TF_VAR_* by the harness. All three ids are
# assigned by AWS, so they cannot be constants the fixtures agree on.
output "support_vpc_id" {
  value = aws_vpc.support.id
}

output "support_subnet_a_id" {
  value = aws_subnet.support_a.id
}

output "support_subnet_b_id" {
  value = aws_subnet.support_b.id
}
