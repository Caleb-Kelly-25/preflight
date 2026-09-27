# Fixture for deriving aws_route53_record.
#
# NEEDS A SUPPORT FIXTURE, and is the first to need one of its OUTPUTS:
#
#   make derive TYPE=aws_route53_record \
#     FIXTURE=./derivefixtures/aws_route53_record \
#     SUPPORT=./derivefixtures/support/route53_zone
#
# A hosted zone id is assigned by AWS, so unlike the SNS and CloudWatch support
# resources it cannot be referred to by a name the two fixtures agree on. The
# support stack exports it and the harness passes it as TF_VAR_support_zone_id.
#
# A `data "aws_route53_zone"` lookup here would have been simpler and WRONG: data
# sources are read under the scratch role, so route53:ListHostedZonesByName would be
# discovered by the loop and attributed to this resource type, which does not need
# it.
#
# The record is inside a .invalid zone that can never resolve, and points at a
# documentation-reserved address (RFC 5737 TEST-NET-1), so it cannot direct traffic
# anywhere even in principle.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # No default_tags: a record set takes no tags.
}

variable "support_zone_id" {
  description = "Zone id exported by derivefixtures/support/route53_zone."
  type        = string
}

resource "aws_route53_record" "probe" {
  zone_id = var.support_zone_id
  name    = "probe.preflight-derive-support.invalid"
  type    = "A"
  ttl     = 300
  records = ["192.0.2.1"]
}
