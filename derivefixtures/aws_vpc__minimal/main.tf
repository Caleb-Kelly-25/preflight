# Minimal variant of the aws_vpc fixture, existing to MEASURE A `when` GATE.
#
# `when` is the only schema feature that can introduce a false pass, because it
# removes actions from a requirement list. Every gate shipped so far is reasoned
# rather than measured, for a structural reason: each fixture sets all of its
# attributes, so every gate was true throughout the derivation and none was ever
# exercised in the negative direction.
#
# Deriving the same resource type from two fixtures answers that. The difference
# between the maximal result and this one IS the gate:
#
#   derivefixtures/aws_vpc          tags set, non-default DNS  -> needs ec2:CreateTags
#   derivefixtures/aws_vpc__minimal no tags, default DNS       -> should NOT
#
# If an untagged VPC still requires ec2:CreateTags, the gate in mappings/ec2.yaml
# is wrong and is under-reporting today for every untagged VPC.
#
# NOTE THE ABSENT `default_tags`. The maximal fixture sets one so that leaked
# resources can be swept by tag, but default_tags applies to every resource in
# the configuration — which would tag this VPC and silently destroy the very
# thing being measured. Identification here falls back to the CIDR below.
#
# Run via: make derive TYPE=aws_vpc FIXTURE=./derivefixtures/aws_vpc__minimal

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags. See above.
}

resource "aws_vpc" "probe" {
  # A distinct range from the maximal fixture, so the two can never be confused
  # if one leaks.
  cidr_block = "10.43.0.0/16"

  # No tags. No enable_dns_hostnames. Nothing optional at all — that is the
  # entire point of this variant.
}
