# MAXIMAL create fixture for aws_ecs_cluster.
#
# One of the cheapest types in the database: an empty cluster is free, creates in
# about a second, has no dependencies, and deletes immediately.
#
# Pairs with aws_ecs_cluster__minimal to measure the tag gate. The same fixture
# also derives the DELETE path — no second fixture needed, since a delete
# derivation applies with operator credentials and destroys under the scratch role.
#
# Run: make derive TYPE=aws_ecs_cluster FIXTURE=./derivefixtures/aws_ecs_cluster

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

resource "aws_ecs_cluster" "probe" {
  name = "preflight-derive-cluster"

  tags = {
    Name = "preflight-derive-cluster"
    env  = "derive"
  }
}
