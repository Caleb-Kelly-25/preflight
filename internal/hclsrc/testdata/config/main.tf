# Fixture for the address -> file:line index. Line numbers are asserted in
# hclsrc_test.go, so inserting a line here means updating that test.

resource "aws_iam_role" "deploy" {
  name = "deploy"
}

# count expands one block into aws_s3_bucket.logs[0..2]; all three must map
# back to this one declaration.
resource "aws_s3_bucket" "logs" {
  count  = 3
  bucket = "logs-${count.index}"
}

# for_each expands into string-keyed instances, including a key containing the
# punctuation the address parser has to survive.
resource "aws_s3_bucket" "by_region" {
  for_each = toset(["eu-west-1", "a.b[0]"])
  bucket   = "data-${each.key}"
}

module "logging" {
  source = "./modules/logging"
}

# The same local module called twice proves the walk is not memoised by
# directory: both prefixes have to be indexed.
module "fleet" {
  source = "./modules/logging"
  count  = 2
}

# A registry module. Its resources are deliberately not indexed — see descend.
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}

# A module whose source is not knowable without evaluating variables.
module "dynamic" {
  source = var.module_source
}

# Data sources produce no findings, so they are not indexed.
data "aws_caller_identity" "current" {}
