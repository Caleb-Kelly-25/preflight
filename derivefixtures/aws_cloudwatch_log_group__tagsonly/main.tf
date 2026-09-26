# TAGS-ONLY variant of the aws_cloudwatch_log_group fixture.
#
# Its whole job is to ATTRIBUTE one of the two gates. Tags are set and
# retention_in_days is not, so if this run still requires logs:PutRetentionPolicy
# then that gate is wrong — the provider writes a retention policy regardless, and
# the entry under-reports for everyone who leaves retention at its default
# ("never expire").
#
# default_tags is kept here on purpose, unlike in the __minimal variant: this
# fixture WANTS the resource tagged. It is the retention gate under test.
#
# Run: make derive TYPE=aws_cloudwatch_log_group FIXTURE=./derivefixtures/aws_cloudwatch_log_group__tagsonly

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

# Tagged, but no retention_in_days. Exactly one attribute differs from maximal.
resource "aws_cloudwatch_log_group" "probe" {
  name = "preflight-derive-lg-tags"

  tags = {
    Name = "preflight-derive-lg-tags"
    env  = "derive"
  }
}
