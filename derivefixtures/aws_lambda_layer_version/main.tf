# Fixture for deriving aws_lambda_layer_version.
#
# SELF-CONTAINED — no support fixture. A layer version needs only a zip, which is
# built in-process by hashicorp/archive from inline content, so nothing has to exist
# beforehand and no binary is committed.
#
# COSTS NOTHING. A layer version stores a few hundred bytes and is never attached to
# a function or invoked.
#
# NO MINIMAL VARIANT, because this entry has no `when` gates: a layer version takes
# no tags and nothing optional that changes the action list. One fixture is the whole
# measurement.
#
# THIS ENTRY HAS A KNOWN ARN DEFECT and the derivation will not fix it. delete and
# get authorise against the VERSIONED layer ARN (layer:name:3), which is unknowable
# at plan time, so the entry deliberately templates the versioned shape and lets it
# degrade to "*" rather than naming the unversioned shape, which would be BROADER
# than what AWS authorises and could produce a false pass. Measuring the action list
# says nothing about that; see the entry's notes.
#
# Run: make derive TYPE=aws_lambda_layer_version FIXTURE=./derivefixtures/aws_lambda_layer_version

terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
    archive = {
      source = "hashicorp/archive"
    }
  }
}

provider "aws" {
  # No default_tags: a layer version takes no tags.
}

data "archive_file" "layer" {
  type        = "zip"
  output_path = "${path.module}/layer.zip"

  source {
    # Layer content conventionally lives under a runtime-specific directory. Nothing
    # ever loads this; it exists so the zip is not empty.
    filename = "nodejs/node_modules/preflight-derive/index.js"
    content  = "module.exports = {};"
  }
}

resource "aws_lambda_layer_version" "probe" {
  layer_name          = "preflight-derive-layer"
  filename            = data.archive_file.layer.output_path
  source_code_hash    = data.archive_file.layer.output_base64sha256
  compatible_runtimes = ["nodejs20.x"]
  description         = "preflight mapping derivation; safe to delete"
}
