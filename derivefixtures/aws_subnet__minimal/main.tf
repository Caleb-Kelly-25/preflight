# MINIMAL variant of aws_subnet. Nothing optional set.
#
# NEEDS THE VPC SUPPORT FIXTURE:
#
#   make derive TYPE=aws_subnet FIXTURE=./derivefixtures/aws_subnet__minimal #     SUPPORT=./derivefixtures/support/vpc
#
# No tags, no public-IP mapping. NOTE THE ABSENT default_tags: it applies to every
# resource in the configuration, so keeping it would silently tag the subnet and
# measure nothing.
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
  # Deliberately no default_tags. See above.
}

resource "aws_subnet" "probe" {
  vpc_id     = var.support_vpc_id
  cidr_block = "10.42.102.0/24"
}
