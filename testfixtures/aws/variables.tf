variable "name_prefix" {
  description = "Prefix for every fixture resource name. Also the S3 ARN prefix the test policies are scoped to. Nothing with this prefix is ever created, only referenced in policy."
  type        = string
  default     = "preflight-contract"

  validation {
    condition     = can(regex("^[a-z0-9-]{3,32}$", var.name_prefix))
    error_message = "name_prefix must be 3-32 lowercase alphanumeric or hyphen characters."
  }
}

variable "condition_region" {
  description = "The region value the condition-key fixture requires. Experiments E6 and E7 supply and withhold this."
  type        = string
  default     = "us-east-1"
}
