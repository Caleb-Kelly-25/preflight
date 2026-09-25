# Fixture for deriving aws_security_group's required IAM actions.
#
# Deliberately exercises the paths a default security group would NOT:
#
#   - tags, so ec2:CreateTags is in play
#   - an explicit ingress rule, so AuthorizeSecurityGroupIngress is in play
#   - an explicit egress rule, which matters twice: Terraform must authorise the
#     rule AND revoke the allow-all egress rule AWS attaches to every new group.
#     A fixture without egress would derive a set missing RevokeSecurityGroupEgress
#     and silently under-report for everyone who restricts egress.
#
# No vpc_id, so the group lands in the account's default VPC. That is on purpose:
# creating a VPC here would pull ec2:CreateVpc into the derived set, where it
# belongs to the fixture rather than to this resource type.
#
# Run via: make derive TYPE=aws_security_group FIXTURE=./derivefixtures/aws_security_group

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

resource "aws_security_group" "probe" {
  name        = "preflight-derive-sg"
  description = "preflight mapping derivation; safe to delete"

  ingress {
    description = "probe"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["10.0.0.0/8"]
  }

  egress {
    description = "probe"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["10.0.0.0/8"]
  }

  tags = {
    Name = "preflight-derive-sg"
    env  = "derive"
  }
}
