variable "aws_region" {
  type    = string
  default = "ap-northeast-1"
}
variable "vpc_id" {
  type = string
}
variable "private_subnet_id" {
  type = string
}
variable "corporate_cidrs" {
  type = list(string)
}
variable "portal_ami_id" {
  type = string
}
variable "managed_instance_arns" {
  type = list(string)
}
variable "portal_instance_type" {
  type    = string
  default = "t4g.micro"
}
variable "managed_egress_instance_ids" {
  type    = set(string)
  default = []
}

variable "enable_mirror_access" {
  type    = bool
  default = false
}

variable "existing_portal_security_group_ids" {
  type    = list(string)
  default = []
}
variable "existing_portal_instance_profile_name" {
  type    = string
  default = ""
}
variable "create_dcv_security_group" {
  type    = bool
  default = true
}
variable "portal_private_ip" {
  type    = string
  default = null
}
variable "approved_https_egress_cidrs" {
  type    = list(string)
  default = []
}
variable "approved_https_prefix_list_ids" {
  type    = list(string)
  default = []
}
variable "permissions_boundary" {
  type    = string
  default = null
}

variable "dcv_port" {
  description = "DCV native client TCP port; match AWSPORTAL_DCV_PORT on the Portal and DCV installer."
  type        = number
  default     = 8443
  nullable    = false
  validation {
    condition     = var.dcv_port == floor(var.dcv_port) && var.dcv_port >= 1 && var.dcv_port <= 65535 && !contains([22, 3389, 8444], var.dcv_port)
    error_message = "DCV port must be an integer from 1 to 65535, excluding 22, 3389 and the authentication broker port 8444."
  }
}
