variable "username" {
  description = "Username for the LiteLLM user"
  type        = string
}

variable "email" {
  description = "Email address for the LiteLLM user"
  type        = string
  validation {
    condition     = can(regex("^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$", var.email))
    error_message = "Email must be a valid email address."
  }
}

