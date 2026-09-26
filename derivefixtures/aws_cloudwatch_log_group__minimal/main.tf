# MINIMAL variant of the aws_cloudwatch_log_group fixture.
#
# Nothing optional set: no tags, no retention, no KMS key. With the tags-only
# variant it attributes both gates — this one establishes that an untagged group
# needs no logs:TagResource.
#
# NOTE THE ABSENT default_tags. It applies to every resource in the configuration,
# so a minimal fixture that kept one would silently tag the group and measure
# nothing. That mistake was nearly shipped once and is why the database gates on
# [tags, tags_all] rather than [tags].
#
# Run: make derive TYPE=aws_cloudwatch_log_group FIXTURE=./derivefixtures/aws_cloudwatch_log_group__minimal

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

resource "aws_cloudwatch_log_group" "probe" {
  name = "preflight-derive-lg-min"
}
