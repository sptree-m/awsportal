variable "aws_region" {
  type    = string
  default = "ap-northeast-1"
}

variable "name" {
  type    = string
  default = "awsportal"
}

variable "existing_vpc_id" {
  type = string
}

variable "subnets" {
  description = "Administrator-provided subnets and their effective route tables."
  type = map(object({
    id                = string
    availability_zone = string
    route_table_id    = string
  }))
  validation {
    condition     = length(var.subnets) > 0 && alltrue([for s in values(var.subnets) : s.id != "" && s.route_table_id != ""])
    error_message = "At least one existing subnet and its effective route table are required."
  }
}

variable "transit_gateway_id" {
  type = string
}

variable "compute_profile" {
  description = "Optional approved Launch Template configuration; null checks existing network only."
  type = object({
    subnet_key            = string
    allowed_ipv4_cidrs    = list(string)
    security_group_ids    = list(string)
    instance_profile_name = string
    ami_id                = string
    ami_checksum          = string
    config_parameter      = string
    resource_class        = optional(string, "shared")
    instance_type         = optional(string, "m7i.2xlarge")
    approved_ami_owners   = optional(list(string), ["self"])
  })
  default = null
  validation {
    condition = var.compute_profile == null ? true : (
      length(var.compute_profile.allowed_ipv4_cidrs) > 0 &&
      alltrue([for c in var.compute_profile.allowed_ipv4_cidrs : can(cidrnetmask(c))])
    )
    error_message = "Specify explicit approved IPv4 CIDRs for the compute profile."
  }
}
