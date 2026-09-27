# MAXIMAL create fixture for aws_db_parameter_group.
#
# THE CHEAP CORNER OF RDS. A parameter group costs nothing and creates instantly —
# unlike aws_db_instance, which takes ~10 minutes to create and ~5 to destroy and
# bills real money. This entry is worth measuring; that one probably is not.
#
# TWO GATES, so THREE fixtures, following the aws_cloudwatch_log_group method:
#
#   aws_db_parameter_group            tags + parameter -> both actions
#   aws_db_parameter_group__tagsonly  tags, NO parameter -> ModifyDBParameterGroup gate
#   aws_db_parameter_group__minimal   neither -> tag gate
#
# Dropping both attributes at once would only show the two actions becoming
# unnecessary together, which does not establish that ModifyDBParameterGroup is
# keyed to `parameter` rather than required on every create.
#
# Run: make derive TYPE=aws_db_parameter_group FIXTURE=./derivefixtures/aws_db_parameter_group

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
  name   = "preflight-derive-pg"
  family = "mysql8.0"

  # A parameter block, which is what the rds:ModifyDBParameterGroup gate keys on.
  # apply_method is pending-reboot because character_set_server is a static
  # parameter; this group is attached to no instance, so nothing ever reboots.
  parameter {
    name         = "character_set_server"
    value        = "utf8mb4"
    apply_method = "pending-reboot"
  }

  tags = {
    Name = "preflight-derive-pg"
    env  = "derive"
  }
}
