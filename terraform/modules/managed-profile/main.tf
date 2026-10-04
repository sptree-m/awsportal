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
      volume_size           = var.resource_class == "windows-import" ? 1024 : 100
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
        volume_size           = 1024
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
      condition     = var.resource_class != "windows-import" || (var.instance_type == "m7i.2xlarge" && data.aws_ami.golden.platform == "windows")
      error_message = "Windows import requires a Windows Golden AMI and m7i.2xlarge."
    }
  }

}
output "approved_profile" {
  value = {
    launch_template_id = aws_launch_template.compute.id, launch_template_version = tostring(aws_launch_template.compute.latest_version), ami_id = var.ami_id, ami_checksum = var.ami_checksum
  }
}
