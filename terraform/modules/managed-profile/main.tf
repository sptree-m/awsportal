terraform {

  required_version = ">= 1.7.0"
  required_providers {
    aws = {
      source = "hashicorp/aws", version = "~> 5.0"
    }
  }

}
variable "name" {
  type = string
}
variable "ami_id" {
  type = string
}
variable "ami_checksum" {
  type = string
}
variable "resource_class" {
  type = string
  validation {
    condition     = contains(["shared", "windows-import"], var.resource_class)
    error_message = "Use separate Shared and Windows profiles."
  }

}
variable "instance_type" {
  type    = string
  default = "m7i.2xlarge"
  validation {
    condition     = can(regex("^(m7i|m7a|m7g|c7i|r7i)\\.", var.instance_type))
    error_message = "Approved CPU instance families only."
  }

}
variable "subnet_id" {
  type = string
}
variable "security_group_ids" {
  type = list(string)
}
variable "instance_profile_name" {
  type = string
}
variable "config_parameter" {
  type = string
  validation {
    condition     = can(regex("^/[A-Za-z0-9_./-]+$", var.config_parameter))
    error_message = "Approved SSM parameter path required."
  }
}
data "aws_ami" "golden" {
  owners = ["self"]
  filter {
    name   = "image-id"
    values = [var.ami_id]
  }
}
resource "aws_launch_template" "compute" {

  name_prefix   = "${var.name}-"
  image_id      = var.ami_id
  instance_type = var.instance_type
  iam_instance_profile {
    name = var.instance_profile_name
  }
  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  network_interfaces {

    device_index                = 0
    subnet_id                   = var.subnet_id
    security_groups             = var.security_group_ids
    associate_public_ip_address = false

  }
  block_device_mappings {

    device_name = data.aws_ami.golden.root_device_name
    ebs {
      volume_size           = var.resource_class == "windows-import" ? var.windows_root_gib : var.shared_root_gib
      iops                  = var.root_iops
      throughput            = var.root_throughput
      volume_type           = "gp3"
      encrypted             = true
      delete_on_termination = true
    }

  }
  dynamic "block_device_mappings" {

    for_each = var.resource_class == "shared" ? [1] : []
    content {
      device_name = "/dev/sdf"
      ebs {
        volume_size           = var.scratch_gib
        iops                  = var.scratch_iops
        throughput            = var.scratch_throughput
        volume_type           = "gp3"
        encrypted             = true
        delete_on_termination = true
      }

    }

  }
  user_data = base64encode(var.resource_class == "shared" ? "#!/bin/bash\nset -euo pipefail\n/usr/bin/python3 /usr/local/libexec/awsportal-dcv-bootstrap --config-parameter '${var.config_parameter}'\n" : "<powershell>\n& 'C:\\ProgramData\\awsportal-import\\Bootstrap-Import.ps1' -ConfigParameter '${var.config_parameter}'\n</powershell>")
  tags = {
    ManagedBy = "awsportal", GoldenChecksum = var.ami_checksum
  }
  lifecycle {
    create_before_destroy = true
    precondition {
      condition     = var.windows_root_gib >= 1024 && var.windows_root_gib <= 16384 && var.shared_root_gib >= 16 && var.scratch_gib >= 1 && var.scratch_gib <= 16384 && var.root_iops >= 3000 && var.root_iops <= 16000 && var.scratch_iops >= 3000 && var.scratch_iops <= 16000 && var.root_throughput >= 125 && var.root_throughput <= 1000 && var.scratch_throughput >= 125 && var.scratch_throughput <= 1000 && var.root_throughput * 4 <= var.root_iops && var.scratch_throughput * 4 <= var.scratch_iops
      error_message = "Approved gp3 size, IOPS and throughput limits required."
    }
    precondition {
      condition     = length(var.security_group_ids) > 0 && alltrue([for sg in data.aws_security_group.approved : sg.vpc_id == data.aws_subnet.approved.vpc_id]) && data.aws_route_table.approved.vpc_id == data.aws_subnet.approved.vpc_id
      error_message = "Subnet, SGs and route table must belong to the approved VPC."
    }
    precondition {
      condition     = alltrue([for c in local.allowed_ipv4_cidrs : cidrhost("${cidrhost(c, 0)}/${split("/", data.aws_subnet.approved.cidr_block)[1]}", 0) == cidrhost(data.aws_subnet.approved.cidr_block, 0) && tonumber(split("/", c)[1]) >= tonumber(split("/", data.aws_subnet.approved.cidr_block)[1])])
      error_message = "IP allocations must fit inside the approved subnet."
    }
    precondition {
      condition     = var.resource_class != "windows-import" || (var.instance_type == "m7i.2xlarge" && data.aws_ami.golden.platform == "windows")
      error_message = "Windows import requires a Windows Golden AMI and m7i.2xlarge."
    }
  }

}
output "approved_profile" {
  value = {
    launch_template_id = aws_launch_template.compute.id, launch_template_version = tostring(aws_launch_template.compute.latest_version), ami_id = var.ami_id, ami_checksum = var.ami_checksum, network = { vpc_id = data.aws_subnet.approved.vpc_id, subnet_id = var.subnet_id, security_group_ids = var.security_group_ids, allowed_ipv4_cidrs = local.allowed_ipv4_cidrs, route_table_id = var.route_table_id, transit_gateway_id = var.transit_gateway_id }
  }
}

variable "allowed_ipv4_cidrs" {
  description = "Approved primary IPv4 allocation ranges within the subnet. Empty uses the full subnet CIDR."
  type        = list(string)
  default     = []
  validation {
    condition     = alltrue([for c in var.allowed_ipv4_cidrs : can(cidrnetmask(c))])
    error_message = "IPv4 CIDRs required."
  }
}
variable "route_table_id" {
  type = string
}
variable "transit_gateway_id" {
  type    = string
  default = ""
}
data "aws_subnet" "approved" { id = var.subnet_id }
data "aws_route_table" "approved" { route_table_id = var.route_table_id }
data "aws_security_group" "approved" {
  for_each = toset(var.security_group_ids)
  id       = each.value
}
locals {
  allowed_ipv4_cidrs = length(var.allowed_ipv4_cidrs) == 0 ? [data.aws_subnet.approved.cidr_block] : var.allowed_ipv4_cidrs
}

variable "windows_root_gib" {
  type    = number
  default = 1024
}

variable "shared_root_gib" {
  type    = number
  default = 100
}

variable "scratch_gib" {
  type    = number
  default = 1024
}

variable "root_iops" {
  type    = number
  default = 3000
}

variable "root_throughput" {
  type    = number
  default = 125
}

variable "scratch_iops" {
  type    = number
  default = 3000
}

variable "scratch_throughput" {
  type    = number
  default = 125
}
