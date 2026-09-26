# MAXIMAL create fixture for aws_cloudwatch_log_group.
#
# THIS TYPE HAS TWO GATES, and that is why it takes THREE fixtures rather than the
# usual two:
#
#   logs:TagResource        when tags are set
#   logs:PutRetentionPolicy when retention_in_days is set
#
# A maximal/minimal pair alone cannot attribute them. Dropping both attributes at
# once shows both actions become unnecessary together, which does not establish
# that each gate is keyed to the right attribute — PutRetentionPolicy might
# actually be required whenever the provider creates any group, and the pair would
# not reveal it.
#
#   aws_cloudwatch_log_group                 tags + retention -> both actions
#   aws_cloudwatch_log_group__tagsonly       tags, NO retention -> retention gate
#   aws_cloudwatch_log_group__minimal        neither -> tag gate
#
# kms_key_id is deliberately NOT set, although it carries a third gate. Doing so
# needs a KMS key, and a KMS key cannot be deleted for at least seven days and
# bills throughout — so it would leave billable residue on every attempt and break
# the harness's clean-account contract. That gate stays reasoning, and the entry
# should say so.
#
# Run: make derive TYPE=aws_cloudwatch_log_group FIXTURE=./derivefixtures/aws_cloudwatch_log_group

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

resource "aws_cloudwatch_log_group" "probe" {
  name              = "preflight-derive-lg"
  retention_in_days = 1

  tags = {
    Name = "preflight-derive-lg"
    env  = "derive"
  }
}
