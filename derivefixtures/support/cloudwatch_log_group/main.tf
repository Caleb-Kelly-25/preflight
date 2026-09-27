# SUPPORT fixture: a CloudWatch log group for aws_cloudwatch_log_stream to live in.
#
# NOT MEASURED. Applied once with OPERATOR credentials and destroyed after the run.
#
# Creating the group inside the measured fixture would pull logs:CreateLogGroup,
# logs:DescribeLogGroups and logs:PutRetentionPolicy into the measurement and
# attribute them to aws_cloudwatch_log_stream, which needs none of them.
#
# retention_in_days keeps the group from being billed for stored data indefinitely
# if a run ever leaks it.

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

resource "aws_cloudwatch_log_group" "support" {
  name              = "preflight-derive-support-lg"
  retention_in_days = 1
}
