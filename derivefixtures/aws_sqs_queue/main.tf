# MAXIMAL create fixture for aws_sqs_queue.
#
# USES name_prefix RATHER THAN name, and that is not a stylistic choice. SQS
# refuses to create a queue with the name of one deleted less than SIXTY SECONDS
# ago. The derivation loop is grant -> apply -> destroy repeated a dozen times,
# with each cycle taking a few seconds, so a fixed name would collide on every
# attempt after the first and the run would fail for a reason that has nothing to
# do with IAM.
#
# name_prefix makes Terraform generate a fresh name per apply, which sidesteps the
# cooldown using a supported provider feature rather than a trick like uuid() in
# the name — that would make every plan non-deterministic and can produce
# "provider produced inconsistent result" errors.
#
# It also exercises `arn_prefix_attributes`, the schema field that builds a
# representative ARN when the real name is generated at apply time. That path is
# otherwise untested against a real apply.
#
# Pairs with aws_sqs_queue__minimal to measure the tag gate.
#
# Run: make derive TYPE=aws_sqs_queue FIXTURE=./derivefixtures/aws_sqs_queue

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

resource "aws_sqs_queue" "probe" {
  name_prefix = "preflight-derive-q-"

  # Ordinary non-default attributes. All are CreateQueue parameters rather than
  # separate API calls, so they are not expected to add actions — setting them
  # checks that expectation rather than assuming it.
  visibility_timeout_seconds = 60
  message_retention_seconds  = 120

  tags = {
    Name = "preflight-derive-queue"
    env  = "derive"
  }
}
