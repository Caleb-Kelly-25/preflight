# MINIMAL variant of the aws_sns_topic fixture, existing to MEASURE THE TAG GATE.
#
# `when` is the only schema feature that can introduce a false pass, because it
# REMOVES actions. A gate is only reasoning until the negative case has been run:
# if an untagged topic still requires sns:TagResource, the gate is wrong and the
# entry under-reports for everyone who does not tag.
#
# NOTE THE ABSENT default_tags. The maximal fixtures set one so leaked resources
# can be swept by tag, but default_tags applies to EVERY resource in the
# configuration — a minimal fixture that keeps it silently tags the resource and
# measures nothing. That mistake was nearly shipped once, and it is the reason the
# database gates on [tags, tags_all] rather than [tags].
#
# Run: make derive TYPE=aws_sns_topic FIXTURE=./derivefixtures/aws_sns_topic__minimal

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  # Deliberately no default_tags. See above.
}

# No tags. Nothing optional. That is the whole point.
resource "aws_sns_topic" "probe" {
  name = "preflight-derive-topic-min"
}
