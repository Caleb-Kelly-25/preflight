# TAGS-ONLY variant of aws_db_parameter_group, to ATTRIBUTE one of two gates.
#
# Tags are set and no `parameter` block is present, so if this run still requires
# rds:ModifyDBParameterGroup then that gate is wrong — the provider modifies the
# group regardless, and the entry under-reports for everyone who creates a group
# without overriding a parameter.
#
# default_tags is kept on purpose, unlike in __minimal: this fixture WANTS the
# resource tagged. It is the parameter gate under test.
#
# Run: make derive TYPE=aws_db_parameter_group FIXTURE=./derivefixtures/aws_db_parameter_group__tagsonly

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

resource "aws_db_parameter_group" "probe" {
  name   = "preflight-derive-pg-tags"
  family = "mysql8.0"

  tags = {
    Name = "preflight-derive-pg-tags"
    env  = "derive"
  }
}
