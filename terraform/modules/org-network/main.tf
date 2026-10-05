terraform {
  required_version = ">= 1.7.0"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}
variable "name" {
  type = string
}
variable "existing_vpc_id" {
  type    = string
  default = ""
}
variable "vpc_cidr" {
  type    = string
  default = null
}
variable "ipv4_ipam_pool_id" {
  type    = string
  default = null
}
variable "ipv4_netmask_length" {
  type    = number
  default = null
}
variable "subnets" {
  description = "Each subnet independently uses an existing ID or creates an approved CIDR. Existing route tables remain read-only."
  type = map(object({
    id                = optional(string, "")
    availability_zone = string
    cidr              = optional(string)
    route_table_id    = optional(string, "")
    tgw_routes        = optional(set(string), [])
  }))
  validation {
    condition     = length(var.subnets) > 0 && alltrue([for s in values(var.subnets) : (s.id != "" && s.cidr == null && length(s.tgw_routes) == 0) || (s.id == "" && s.cidr != null && (s.route_table_id == "" || length(s.tgw_routes) == 0))])
    error_message = "Choose an existing subnet or a new CIDR; TGW routes may only be installed in new route tables."
  }
}
variable "transit_gateway_id" {
  type    = string
  default = ""
}
variable "create_tgw_attachment" {
  type    = bool
  default = false
}
variable "tgw_attachment_subnet_keys" {
  type    = set(string)
  default = []
}
locals {
  new_subnets      = { for k, s in var.subnets : k => s if s.id == "" }
  existing_subnets = { for k, s in var.subnets : k => s if s.id != "" }
  new_tables       = { for k, s in local.new_subnets : k => s if s.route_table_id == "" }
  vpc_id           = var.existing_vpc_id != "" ? var.existing_vpc_id : aws_vpc.private[0].id
  subnet_ids       = merge({ for k, s in aws_subnet.private : k => s.id }, { for k, s in data.aws_subnet.provided : k => s.id })
  route_table_ids  = merge({ for k, r in aws_route_table.private : k => r.id }, { for k, s in var.subnets : k => s.route_table_id if s.route_table_id != "" })
  routes           = { for pair in flatten([for k, s in local.new_tables : [for c in s.tgw_routes : { key = "${k}/${c}", subnet = k, cidr = c }]]) : pair.key => pair }
}
resource "aws_vpc" "private" {
  count                = var.existing_vpc_id == "" ? 1 : 0
  cidr_block           = var.vpc_cidr
  ipv4_ipam_pool_id    = var.ipv4_ipam_pool_id
  ipv4_netmask_length  = var.ipv4_netmask_length
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.name, ManagedBy = "awsportal" }
  lifecycle {
    precondition {
      condition     = (var.vpc_cidr != null) != (var.ipv4_ipam_pool_id != null && var.ipv4_netmask_length != null)
      error_message = "New VPC requires either an approved CIDR or IPAM pool/netmask."
    }
  }
}
data "aws_vpc" "approved" { id = local.vpc_id }
data "aws_subnet" "provided" {
  for_each = local.existing_subnets
  id       = each.value.id
}
resource "aws_subnet" "private" {
  for_each                        = local.new_subnets
  vpc_id                          = local.vpc_id
  availability_zone               = each.value.availability_zone
  cidr_block                      = each.value.cidr
  map_public_ip_on_launch         = false
  assign_ipv6_address_on_creation = false
  tags                            = { Name = "${var.name}-${each.key}", ManagedBy = "awsportal" }
}
resource "aws_route_table" "private" {
  for_each = local.new_tables
  vpc_id   = local.vpc_id
  tags     = { Name = "${var.name}-${each.key}", ManagedBy = "awsportal" }
}
# Only associations for newly created subnets are managed. Never re-associate a
# provided subnet, modify its routes or administer the organization's TGW table.
resource "aws_route_table_association" "private" {
  for_each       = local.new_subnets
  subnet_id      = local.subnet_ids[each.key]
  route_table_id = local.route_table_ids[each.key]
}
resource "aws_route" "tgw" {
  for_each               = local.routes
  route_table_id         = aws_route_table.private[each.value.subnet].id
  destination_cidr_block = each.value.cidr
  transit_gateway_id     = var.transit_gateway_id
  depends_on             = [aws_ec2_transit_gateway_vpc_attachment.private]
  lifecycle {
    precondition {
      condition     = var.transit_gateway_id != ""
      error_message = "Approved TGW ID is required for TGW routes."
    }
  }
}
resource "aws_ec2_transit_gateway_vpc_attachment" "private" {
  count                                           = var.create_tgw_attachment ? 1 : 0
  vpc_id                                          = local.vpc_id
  transit_gateway_id                              = var.transit_gateway_id
  subnet_ids                                      = [for k in var.tgw_attachment_subnet_keys : local.subnet_ids[k]]
  transit_gateway_default_route_table_association = false
  transit_gateway_default_route_table_propagation = false
  tags                                            = { Name = var.name, ManagedBy = "awsportal" }
  lifecycle {
    precondition {
      condition     = var.transit_gateway_id != "" && length(var.tgw_attachment_subnet_keys) > 0
      error_message = "Attachment requires approved TGW and explicitly selected subnets."
    }
  }
}
check "provided_network" {
  assert {
    condition     = alltrue([for k, s in data.aws_subnet.provided : s.vpc_id == local.vpc_id && s.availability_zone == var.subnets[k].availability_zone])
    error_message = "Provided subnets must match the approved VPC and AZ."
  }
}
output "vpc_id" { value = local.vpc_id }
output "subnet_ids" { value = local.subnet_ids }
output "route_table_ids" { value = local.route_table_ids }
output "ownership" {
  value = { vpc_created = var.existing_vpc_id == "", created_subnet_keys = keys(local.new_subnets), created_route_table_keys = keys(local.new_tables), tgw_attachment_created = var.create_tgw_attachment }
}

variable "interface_endpoint_services" {
  type    = set(string)
  default = []
  validation {
    condition     = alltrue([for s in var.interface_endpoint_services : contains(["ssm", "ssmmessages", "ec2messages", "logs", "kms", "ec2", "states", "monitoring", "s3"], s)])
    error_message = "Choose approved management endpoint services."
  }
}
variable "existing_endpoint_security_group_id" {
  type    = string
  default = ""
}
variable "endpoint_client_cidrs" {
  type    = list(string)
  default = []
}
variable "create_s3_gateway_endpoint" {
  type    = bool
  default = false
}
variable "s3_endpoint_policy" {
  type    = string
  default = null
}
data "aws_region" "current" {}
resource "aws_security_group" "endpoint" {
  count       = length(var.interface_endpoint_services) > 0 && var.existing_endpoint_security_group_id == "" ? 1 : 0
  name_prefix = "${var.name}-endpoint-"
  vpc_id      = local.vpc_id
  ingress {
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = var.endpoint_client_cidrs
  }
  egress = []
}
resource "aws_vpc_endpoint" "management" {
  for_each            = var.interface_endpoint_services
  vpc_id              = local.vpc_id
  service_name        = "com.amazonaws.${data.aws_region.current.name}.${each.value}"
  vpc_endpoint_type   = "Interface"
  private_dns_enabled = true
  subnet_ids          = values(local.subnet_ids)
  security_group_ids  = [var.existing_endpoint_security_group_id != "" ? var.existing_endpoint_security_group_id : aws_security_group.endpoint[0].id]
}
resource "aws_vpc_endpoint" "s3" {
  count             = var.create_s3_gateway_endpoint ? 1 : 0
  vpc_id            = local.vpc_id
  service_name      = "com.amazonaws.${data.aws_region.current.name}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [for r in aws_route_table.private : r.id]
  policy            = var.s3_endpoint_policy
  lifecycle {
    precondition {
      condition     = length(local.new_tables) > 0 && var.s3_endpoint_policy != null
      error_message = "S3 gateway endpoint requires newly owned route tables and an approved bucket policy. Existing tables must be configured by IT."
    }
  }
}
output "endpoint_security_group_id" { value = var.existing_endpoint_security_group_id != "" ? var.existing_endpoint_security_group_id : try(aws_security_group.endpoint[0].id, null) }
output "s3_prefix_list_id" { value = try(aws_vpc_endpoint.s3[0].prefix_list_id, null) }
