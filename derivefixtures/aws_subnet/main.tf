# MAXIMAL create fixture for aws_subnet.
#
# NEEDS THE VPC SUPPORT FIXTURE:
#
#   make derive TYPE=aws_subnet FIXTURE=./derivefixtures/aws_subnet #     SUPPORT=./derivefixtures/support/vpc
#
# Free, fast, and one of the most widely used AWS resource types. The subnet is
# created inside the SUPPORT VPC; the support stack's own two subnets are separate
# and are there for aws_db_subnet_group.
#
# TWO GATES, so THREE fixtures — a maximal/minimal pair would only show both actions
# becoming unnecessary together:
#
#   aws_subnet              tags + map_public_ip_on_launch -> both actions
#   aws_subnet__tagsonly    tags only -> attributes the ModifySubnetAttribute gate
#   aws_subnet__minimal     neither -> attributes the tag gate
#
# AND ONE OF THOSE GATES IS ON A BOOLEAN, which CLAUDE.md flags as the shape most
# likely to be a false pass. `attribute_set` reads `false` as UNSET, so a gate on a
# boolean is only safe when the attribute defaults to false — and
# map_public_ip_on_launch does, which is why the docs call this gate "correct only by
# luck". Measuring it converts luck into evidence.
#
# The other thing this run should settle is ec2:DescribeTags in the read set.
# CLAUDE.md records it as having been a surplus guess TWICE (aws_vpc,
# aws_security_group), because the provider reads tags back from each resource's own
# Describe call. If it is surplus here too, that is three for three.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

variable "support_vpc_id" {
  description = "VPC id exported by derivefixtures/support/vpc."
  type        = string
}

provider "aws" {
  default_tags {
    tags = {
      preflight-derive = "true"
    }
  }
}

resource "aws_subnet" "probe" {
  vpc_id     = var.support_vpc_id
  cidr_block = "10.42.100.0/24"

  # The boolean the ec2:ModifySubnetAttribute gate keys on.
  map_public_ip_on_launch = true

  tags = {
    Name = "preflight-derive-subnet"
    env  = "derive"
  }
}
