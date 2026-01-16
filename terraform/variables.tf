variable "aws_region" {
  type        = string
  description = "AWS region to deploy into."
  default     = "us-west-2"
}

variable "dynamodb_table_name" {
  type        = string
  description = "Name of the DynamoDB table used for polls."
  default     = "bff-hang"
}

variable "lambda_role_name" {
  type        = string
  description = "IAM role name for the Lambda function."
  default     = "bff-hang-lambda-role"
}

variable "lambda_function_name" {
  type        = string
  description = "Lambda function name."
  default     = "bff-hang"
}

variable "lambda_package_path" {
  type        = string
  description = "Path to the zipped Lambda deployment package (zip containing bootstrap)."
}

variable "app_base_url" {
  type        = string
  description = "Public base URL used when rendering share links."
  default     = ""
}

variable "domain_name" {
  type        = string
  description = "Custom domain name for the Lambda HTTP endpoint."
  default     = "bffhang.fun"
}

variable "route53_zone_id" {
  type        = string
  description = "Route53 hosted zone ID for the custom domain."
  default     = "Z0041753160CTNVWUAX4D"
}
