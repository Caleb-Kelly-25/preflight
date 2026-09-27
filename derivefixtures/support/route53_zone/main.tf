# SUPPORT fixture: a Route 53 hosted zone for aws_route53_record to write into.
#
# NOT MEASURED. Applied once with OPERATOR credentials and destroyed after the run.
#
# Creating the zone inside the measured fixture would pull route53:CreateHostedZone,
# route53:GetChange and the tag actions into the measurement and attribute them to
# aws_route53_record.
#
# AWS does not charge for a hosted zone deleted within twelve hours of creation, so
# this costs nothing. The name is a `.invalid` domain, which RFC 2606 reserves so it
# can never resolve — Route 53 does not check that a zone's name is a domain anyone
# owns, so a real-looking name would create an authoritative zone for someone else's
# domain.
#
# force_destroy so the teardown can remove any record the measured fixture left.

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

resource "aws_route53_zone" "support" {
  name          = "preflight-derive-support.invalid"
  comment       = "preflight derivation support; safe to delete"
  force_destroy = true
}

# Handed to the measured fixture as TF_VAR_support_zone_id by the harness. A zone id
# is assigned by AWS, so it cannot be a constant the two fixtures agree on — and a
# `data` lookup in the measured fixture would attribute
# route53:ListHostedZonesByName to aws_route53_record, which does not need it.
output "support_zone_id" {
  value = aws_route53_zone.support.zone_id
}
