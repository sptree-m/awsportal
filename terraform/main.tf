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
  name   = "awsportal"
  vpc_id = var.vpc_id

  ingress {
    description = "HTTPS from corporate network"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = var.corporate_cidrs
  }

  egress {
    description = "HTTPS for approved AWS/API path; production egress must be restricted by network policy"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "dcv_only" {
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
    description = "Temporary baseline; replace with approved egress firewall/proxy path"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_iam_role" "portal" {
  name = "awsportal-role"
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
  role = aws_iam_role.portal.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "ReadInstances"
        Effect   = "Allow"
        Action   = ["ec2:DescribeInstances", "ec2:DescribeInstanceStatus"]
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
  name = "awsportal"
  role = aws_iam_role.portal.name
}

resource "aws_instance" "portal" {
  ami                    = var.portal_ami_id
  instance_type          = var.portal_instance_type
  subnet_id              = var.private_subnet_id
  vpc_security_group_ids = [aws_security_group.portal.id]
  iam_instance_profile   = aws_iam_instance_profile.portal.name

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
