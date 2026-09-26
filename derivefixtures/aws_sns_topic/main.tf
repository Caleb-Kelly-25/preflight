# MAXIMAL create fixture for aws_sns_topic.
#
# Pairs with aws_sns_topic__minimal to measure the tag gate: derive both, and the
# difference between the derived sets IS the gate. SNS makes that pairing worth
# doing on its own account rather than by analogy — tagging has needed its own
# permission in every service measured so far, by three DIFFERENT mechanisms (S3
# makes a separate PutBucketTagging call; IAM and EC2 authorise the tagging action
# as part of the create with no separate call), and which one a service uses is
# not inferable from another service.
#
# SNS is one of the cheapest types in the database to derive: a topic is free,
# creates in under a second, has no dependencies, and unlike SQS it can be
# recreated under the same name immediately after deletion.
#
# Run: make derive TYPE=aws_sns_topic FIXTURE=./derivefixtures/aws_sns_topic

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

resource "aws_sns_topic" "probe" {
  name         = "preflight-derive-topic"
  display_name = "preflight derive"

  tags = {
    Name = "preflight-derive-topic"
    env  = "derive"
  }
}
