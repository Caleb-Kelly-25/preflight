# MAXIMAL create fixture for aws_route53_zone.
#
# THE QUESTION THIS RUN EXISTS TO SETTLE is route53:GetChange. Route 53 returns a
# change id from CreateHostedZone and the provider polls it until the change has
# propagated to all its name servers — a real API call on the create path. The
# entry does NOT list it, and says so in its notes, because GetChange is
# authorised against the `change` resource type rather than the hosted zone and
# no plan attribute holds a change id. If this run needs it, the entry
# under-reports today and the schema gap becomes urgent rather than theoretical.
#
# WHY THIS ENTRY IS THE CHEAPEST ONE LEFT: a public hosted zone creates in
# seconds, needs no VPC and no subnets, and AWS does not charge for a zone deleted
# within twelve hours of creation — so a derivation run that creates and destroys
# one a dozen times costs nothing.
#
# The name is a `.invalid` domain, which RFC 2606 reserves precisely so it can
# never resolve. Route 53 does not check that a zone's name is a domain anyone
# owns, so a real-looking name would create an authoritative zone for someone
# else's domain. Nothing here can affect DNS for anything.
#
# PUBLIC, not private. A private zone must be associated with a VPC, which would
# pull ec2 actions into the measurement and attribute them to this resource type.
#
# Run: make derive TYPE=aws_route53_zone FIXTURE=./derivefixtures/aws_route53_zone

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

resource "aws_route53_zone" "probe" {
  name    = "preflight-derive.invalid"
  comment = "preflight mapping derivation; safe to delete"

  # force_destroy lets the destroy remove any record sets the zone still holds.
  # Without it a teardown can fail on the zone's own NS and SOA records, and a
  # failed teardown aborts the whole run — the harness stops rather than
  # measuring against leftovers.
  force_destroy = true

  tags = {
    Name = "preflight-derive-zone"
    env  = "derive"
  }
}
