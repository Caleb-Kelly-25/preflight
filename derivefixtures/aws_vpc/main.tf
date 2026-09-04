# Fixture for deriving aws_vpc's required IAM actions.
#
# Deliberately minimal, but NOT default: it sets tags and a non-default DNS
# attribute, because the derived set is only valid for the configuration the
# fixture exercises. An untagged VPC with default DNS would derive a smaller set
# and silently under-report for everyone who tags theirs.
#
# Run via: make derive TYPE=aws_vpc FIXTURE=./derivefixtures/aws_vpc

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Region comes from AWS_REGION, which the harness sets.
  default_tags {
    tags = {
      preflight-derive = "true"
    }
  }
}

resource "aws_vpc" "probe" {
  # A range unlikely to collide with anything real in a scratch account.
  cidr_block           = "10.42.0.0/16"
  enable_dns_hostnames = true

  tags = {
    Name = "preflight-derive-vpc"
    env  = "derive"
  }
}
