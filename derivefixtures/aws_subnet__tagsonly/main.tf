# TAGS-ONLY variant of aws_subnet, to ATTRIBUTE the boolean gate.
#
# NEEDS THE VPC SUPPORT FIXTURE:
#
#   make derive TYPE=aws_subnet FIXTURE=./derivefixtures/aws_subnet__tagsonly #     SUPPORT=./derivefixtures/support/vpc
#
# Tags are set and map_public_ip_on_launch is not. If this run still requires
# ec2:ModifySubnetAttribute then that gate is wrong and the entry under-reports for
# every subnet left at the default.
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
  cidr_block = "10.42.101.0/24"

  tags = {
    Name = "preflight-derive-subnet-tags"
    env  = "derive"
  }
}
