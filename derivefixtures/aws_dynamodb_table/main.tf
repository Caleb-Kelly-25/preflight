# MAXIMAL create fixture for aws_dynamodb_table.
#
# On-demand billing means an idle table costs nothing, and creation takes a
# few seconds. One of the most widely used AWS resource types, so worth the four
# fixtures.
#
# THREE GATES ON THIS ENTRY, so FOUR fixtures. tags, ttl and
# point_in_time_recovery each gate a different action, and a maximal/minimal pair
# would only show all three becoming unnecessary together — it could not tell
# "keyed to ttl" from "required on every create". The set is:
#
#   aws_dynamodb_table              tags + ttl + pitr -> all three actions
#   aws_dynamodb_table__nopitr      tags + ttl        -> attributes the pitr gate
#   aws_dynamodb_table__ttlonly     ttl only          -> attributes the tags gate
#   aws_dynamodb_table__minimal     none              -> attributes ttl
#
# restore_source_name and import_table also carry gates and are deliberately NOT
# set: both change the table's entire creation mode (restore-from-backup,
# import-from-S3) rather than adding an attribute, so a fixture using them would
# measure a different operation. Those two gates stay reasoning, and the entry
# should say so.
#
# Run: make derive TYPE=aws_dynamodb_table FIXTURE=./derivefixtures/aws_dynamodb_table

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

resource "aws_dynamodb_table" "probe" {
  name = "preflight-derive-table"

  # PAY_PER_REQUEST so the table costs nothing while idle and needs no capacity
  # planning. PROVISIONED would add throughput settings that are CreateTable
  # parameters, not separate calls, so it would change cost without changing the
  # measurement.
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  # The block the dynamodb:UpdateTimeToLive gate keys on. TTL is NOT a CreateTable
  # parameter, so this is a genuine second API call.
  ttl {
    attribute_name = "expires"
    enabled        = true
  }

  # The block the dynamodb:UpdateContinuousBackups gate keys on. Also a genuine
  # second call.
  point_in_time_recovery {
    enabled = true
  }

  tags = {
    Name = "preflight-derive-table"
    env  = "derive"
  }
}
