# NO-PITR variant of aws_dynamodb_table.
#
# Tags and ttl are set, point_in_time_recovery is NOT. If this run still needs
# dynamodb:UpdateContinuousBackups then that gate is wrong and the entry
# under-reports for every table left at the default backup setting.
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
# Run: make derive TYPE=aws_dynamodb_table FIXTURE=./derivefixtures/aws_dynamodb_table__nopitr

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
  name = "preflight-derive-table-nopitr"

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

  tags = {
    Name = "preflight-derive-table-nopitr"
    env  = "derive"
  }
}
