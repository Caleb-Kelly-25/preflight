# Consumed by the contract tests. Export with:
#   terraform output -json > fixtures.json

output "exact_arn_role" {
  description = "Role whose policy allows s3:CreateBucket on exactly one bucket ARN."
  value       = aws_iam_role.exact_arn.arn
}

output "prefix_arn_role" {
  description = "Role whose policy allows s3:CreateBucket on an ARN prefix."
  value       = aws_iam_role.prefix_arn.arn
}

output "condition_key_role" {
  description = "Role whose allow is gated on aws:RequestedRegion."
  value       = aws_iam_role.condition_key.arn
}

output "pathed_role" {
  description = "Role with a non-default IAM path. Its correct ARN, for comparison against what string parsing alone would reconstruct."
  value       = aws_iam_role.pathed.arn
}

output "pathed_role_naive_arn" {
  description = "What resolving an assumed-role session ARN by string parsing alone would produce for the pathed role. Experiment E8 expects simulating this to fail."
  value       = "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}:role/${aws_iam_role.pathed.name}"
}

output "allowed_bucket_arn" {
  description = "The exact bucket ARN the exact_arn fixture permits. Never created."
  value       = "${local.bucket}-exact-bucket"
}

output "arn_prefix" {
  description = "The ARN prefix the prefix_arn fixture permits."
  value       = "${local.bucket}-*"
}

output "condition_region" {
  description = "The region value the condition-key fixture requires."
  value       = var.condition_region
}

output "test_runner_policy_arn" {
  description = "Attach this to whichever identity runs the contract tests."
  value       = aws_iam_policy.contract_test_runner.arn
}
