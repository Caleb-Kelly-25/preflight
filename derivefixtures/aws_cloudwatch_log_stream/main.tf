# Fixture for deriving aws_cloudwatch_log_stream.
#
# NEEDS A SUPPORT FIXTURE — the log group is not created here:
#
#   make derive TYPE=aws_cloudwatch_log_stream \
#     FIXTURE=./derivefixtures/aws_cloudwatch_log_stream \
#     SUPPORT=./derivefixtures/support/cloudwatch_log_group
#
# Creating the group here would pull logs:CreateLogGroup, logs:DescribeLogGroups and
# logs:PutRetentionPolicy into the measurement and attribute them to the log STREAM,
# which needs none of them.
#
# No gates on this entry and nothing optional to set, so it needs no minimal variant:
# a log stream has a name and a group and that is all.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # No default_tags: a log stream takes no tags.
}

resource "aws_cloudwatch_log_stream" "probe" {
  name           = "preflight-derive-stream"
  log_group_name = "preflight-derive-support-lg"
}
