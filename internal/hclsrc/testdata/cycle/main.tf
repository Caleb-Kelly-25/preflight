resource "aws_s3_bucket" "root" {
  bucket = "root"
}

# A module that calls the directory it lives in. Terraform rejects this; the
# index has to terminate rather than trust that it never happens.
module "self" {
  source = "./"
}
