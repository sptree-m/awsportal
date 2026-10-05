terraform {
  required_version = ">= 1.7.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" { region = var.aws_region }

resource "aws_security_group" "portal" {
  count  = length(var.existing_portal_security_group_ids) == 0 ? 1 : 0
  name   = "awsportal"
  vpc_id = var.vpc_id

  ingress {
    description = "HTTPS from corporate network"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = var.corporate_cidrs
  }

  dynamic "ingress" {
    for_each = aws_security_group.managed_egress
    content {
      description     = "TLS proxy from managed EC2"
      from_port       = 3128
      to_port         = 3128
      protocol        = "tcp"
      security_groups = [ingress.value.id]
    }
  }

  dynamic "ingress" {
    for_each = var.enable_mirror_access ? aws_security_group.managed_egress : {}
    content {
      description     = "HTTPS mirror API and read-only Git from managed EC2"
      from_port       = 443
      to_port         = 443
      protocol        = "tcp"
      security_groups = [ingress.value.id]
    }
  }

  egress {
    description     = "HTTPS for approved AWS/API path; production egress must be restricted by network policy"
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    cidr_blocks     = var.approved_https_egress_cidrs
    prefix_list_ids = var.approved_https_prefix_list_ids
  }
}

resource "aws_security_group" "dcv_only" {
  count  = var.create_dcv_security_group ? 1 : 0
  name   = "managed-dcv-only"
  vpc_id = var.vpc_id

  ingress {
    description = "Amazon DCV only"
    from_port   = 8443
    to_port     = 8443
    protocol    = "tcp"
    cidr_blocks = var.corporate_cidrs
  }

  egress {
    description     = "Temporary baseline; replace with approved egress firewall/proxy path"
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    cidr_blocks     = var.approved_https_egress_cidrs
    prefix_list_ids = var.approved_https_prefix_list_ids
  }
}

resource "aws_iam_role" "portal" {
  count                = var.existing_portal_instance_profile_name == "" ? 1 : 0
  permissions_boundary = var.permissions_boundary
  name                 = "awsportal-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "portal" {
  count = var.existing_portal_instance_profile_name == "" ? 1 : 0
  role  = aws_iam_role.portal[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ReadInstances"
        Effect   = "Allow"
        Action   = ["ec2:DescribeInstances", "ec2:DescribeInstanceStatus", "ec2:DescribeSecurityGroups", "ec2:DescribeNetworkInterfaces"]
        Resource = "*"
      },
      {
        Sid      = "ReadCostExplorer"
        Effect   = "Allow"
        Action   = ["ce:GetCostAndUsage"]
        Resource = "*"
      },
      {
        Sid      = "ControlManagedInstances"
        Effect   = "Allow"
        Action   = ["ec2:StartInstances", "ec2:StopInstances"]
        Resource = var.managed_instance_arns
      }
    ]
  })
}

resource "aws_iam_instance_profile" "portal" {
  count = var.existing_portal_instance_profile_name == "" ? 1 : 0
  name  = "awsportal"
  role  = aws_iam_role.portal[0].name
}

resource "aws_instance" "portal" {
  ami                         = var.portal_ami_id
  instance_type               = var.portal_instance_type
  subnet_id                   = var.private_subnet_id
  private_ip                  = var.portal_private_ip
  associate_public_ip_address = false
  vpc_security_group_ids      = local.portal_security_group_ids
  iam_instance_profile        = var.existing_portal_instance_profile_name != "" ? var.existing_portal_instance_profile_name : aws_iam_instance_profile.portal[0].name

  root_block_device {
    encrypted   = true
    volume_type = "gp3"
    volume_size = 16
  }

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }

  tags = { Name = "awsportal" }
}

resource "aws_security_group" "managed_egress" {
  for_each    = var.managed_egress_instance_ids
  name_prefix = "awsportal-egress-${each.key}-"
  vpc_id      = var.vpc_id
  ingress {
    description = "DCV from corporate network"
    from_port   = 8443
    to_port     = 8443
    protocol    = "tcp"
    cidr_blocks = var.corporate_cidrs
  }
  egress = []
  # No egress at bootstrap. Portal owns subsequent egress rules.
  tags = { "awsportal:egress-instance" = each.key }
  lifecycle { ignore_changes = [egress] }
}
output "managed_egress_security_groups" {
  value = { for instance, group in aws_security_group.managed_egress : instance => group.id }
}
output "proxy_security_group_id" { value = local.portal_security_group_ids[0] }

resource "aws_iam_role_policy" "managed_egress" {
  count = length(var.managed_egress_instance_ids) > 0 && var.existing_portal_instance_profile_name == "" ? 1 : 0
  role  = aws_iam_role.portal[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "ControlManagedEgress"
      Effect   = "Allow"
      Action   = ["ec2:AuthorizeSecurityGroupEgress", "ec2:RevokeSecurityGroupEgress"]
      Resource = [for g in aws_security_group.managed_egress : g.arn]
    }]
  })
}

locals { portal_security_group_ids = length(var.existing_portal_security_group_ids) > 0 ? var.existing_portal_security_group_ids : [aws_security_group.portal[0].id] }
data "aws_subnet" "portal" { id = var.private_subnet_id }
data "aws_security_group" "portal_provided" {
  for_each = toset(var.existing_portal_security_group_ids)
  id       = each.value
}
check "portal_network" {
  assert {
    condition     = data.aws_subnet.portal.vpc_id == var.vpc_id && alltrue([for sg in data.aws_security_group.portal_provided : sg.vpc_id == var.vpc_id])
    error_message = "Portal subnet and SGs must belong to the supplied VPC."
  }
}

moved {
  from = aws_security_group.portal
  to   = aws_security_group.portal[0]
}
moved {
  from = aws_security_group.dcv_only
  to   = aws_security_group.dcv_only[0]
}
moved {
  from = aws_iam_role.portal
  to   = aws_iam_role.portal[0]
}
moved {
  from = aws_iam_role_policy.portal
  to   = aws_iam_role_policy.portal[0]
}
moved {
  from = aws_iam_instance_profile.portal
  to   = aws_iam_instance_profile.portal[0]
}
