# Terraform module example for LiteLLM user management
# This module manages a LiteLLM user and returns the API key

terraform {
  required_version = ">= 1.0"
}

# Data source to execute the manage_user.py script
# Script is in the same directory as this module
data "external" "litellm_user" {
  program = ["python3", "${path.module}/manage_user.py"]

  query = {
    username = var.username
    email    = var.email
  }
}

# Extract values from external data source
locals {
  user_result = data.external.litellm_user.result
  # Extract the key and status
  api_key      = try(local.user_result.status == "completed" ? local.user_result.key : null, null)
  error_message = try(local.user_result.status == "failed" ? local.user_result.message : null, null)
}

# Outputs
output "api_key" {
  description = "LiteLLM API key for the user"
  value       = local.api_key
  sensitive   = true
}

output "error_message" {
  description = "Error message if status is failed"
  value       = local.error_message
}


