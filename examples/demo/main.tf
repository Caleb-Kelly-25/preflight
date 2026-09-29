# A tiny configuration used to demonstrate preflight in CI.
#
# NOTHING HERE IS EVER APPLIED. The committed plan.json beside this file was
# produced with `terraform plan` alone, which creates no resources and costs
# nothing. The CI workflow reads that plan and asks IAM whether a given identity
# *could* apply it — the whole point being that it never touches AWS resources.
#
# The resources are chosen so the demo shows something real. The identity the
# workflow checks (preflight-demo-deploy) holds s3:CreateBucket and nothing else,
# so preflight reports the fourteen S3 read-backs and every IAM action as missing.
# That is not a contrived failure: aws_s3_bucket's create was measured at 17
# actions, 14 of them read-backs, and an entry claiming only s3:CreateBucket is
# exactly the mistake this tool exists to catch.
#
# Every resource here also appears in the plan, so every SARIF result has a source
# location and none are dropped.

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  region = "us-east-1"
}

resource "aws_s3_bucket" "logs" {
  bucket = "preflight-demo-logs-example"

  tags = {
    Name = "preflight-demo-logs"
  }
}

resource "aws_iam_role" "deploy" {
  name = "preflight-demo-example-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}
