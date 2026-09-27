# MINIMAL variant of the aws_ecs_cluster fixture, to MEASURE THE TAG GATE.
#
# If an untagged cluster still requires ecs:TagResource, the gate is wrong and the
# entry under-reports for everyone who does not tag. `when` is the only schema
# feature that can produce a false pass, because it removes actions.
#
# Run: make derive TYPE=aws_ecs_cluster FIXTURE=./derivefixtures/aws_ecs_cluster__minimal

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

resource "aws_ecs_cluster" "probe" {
  name = "preflight-derive-cluster-min"
}
