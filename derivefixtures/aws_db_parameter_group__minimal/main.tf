# MINIMAL variant of aws_db_parameter_group. Nothing optional set.
#
# With the tags-only variant this attributes both gates: here an untagged group
# with no parameter overrides should need neither rds:AddTagsToResource nor
# rds:ModifyDBParameterGroup.
#
# Run: make derive TYPE=aws_db_parameter_group FIXTURE=./derivefixtures/aws_db_parameter_group__minimal

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags: it applies to every resource in the
  # configuration, so keeping it here would silently tag the resource and measure
  # nothing. That mistake was nearly shipped once.
}

resource "aws_db_parameter_group" "probe" {
  name   = "preflight-derive-pg-min"
  family = "mysql8.0"
}
