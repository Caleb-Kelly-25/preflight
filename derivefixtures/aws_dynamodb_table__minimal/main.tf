# MINIMAL variant of aws_dynamodb_table. Nothing optional set.
#
# No tags, no ttl, no point-in-time recovery. Establishes that a bare table
# needs only dynamodb:CreateTable and the read-backs.
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
# Run: make derive TYPE=aws_dynamodb_table FIXTURE=./derivefixtures/aws_dynamodb_table__minimal

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags: it applies to every resource in the
  # configuration, so keeping it here would silently tag the table.
}

resource "aws_dynamodb_table" "probe" {
  name = "preflight-derive-table-min"

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
}
