terraform {
  required_version = ">= 1.7.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnets_by_az" { type = map(string) }
variable "users" {
  description = "Stable Portal User ID => username. Do not recreate EFS on username changes."
  type        = map(string)
  validation {
    condition     = length(var.users) <= 50 && alltrue([for id in keys(var.users) : can(tonumber(id)) && try(tonumber(id) >= 1 && tonumber(id) <= 1000000, false)])
    error_message = "At most 50 stable positive user IDs are supported."
  }
}
variable "golden_ami_id" { type = string }
variable "instance_type" {
  type    = string
  default = "m7i.xlarge"
}
variable "environment_id" { type = string }
variable "group_id" { type = string }
variable "corporate_cidrs" { type = list(string) }
variable "portal_security_group_id" { type = string }
variable "endpoint_security_group_id" { type = string }
variable "s3_prefix_list_id" { type = string }
variable "metrics_bucket_name" { type = string }
variable "dataset_bucket_arn" { type = string }
variable "portal_role_name" { type = string }

resource "aws_security_group" "compute" {
  name_prefix = "${var.name}-compute-"
  vpc_id      = var.vpc_id
  ingress {
    from_port   = 8443
    to_port     = 8443
    protocol    = "tcp"
    cidr_blocks = var.corporate_cidrs
  }
  egress {
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    security_groups = [var.portal_security_group_id, var.endpoint_security_group_id]
  }
  egress {
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    prefix_list_ids = [var.s3_prefix_list_id]
  }
  egress {
    from_port       = 2049
    to_port         = 2049
    protocol        = "tcp"
    security_groups = [aws_security_group.storage.id]
  }
}
resource "aws_security_group_rule" "portal_from_compute" {
  type                     = "ingress"
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  security_group_id        = var.portal_security_group_id
  source_security_group_id = aws_security_group.compute.id
}
resource "aws_security_group_rule" "endpoints_from_compute" {
  type                     = "ingress"
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  security_group_id        = var.endpoint_security_group_id
  source_security_group_id = aws_security_group.compute.id
}
resource "aws_security_group" "storage" {
  name_prefix = "${var.name}-efs-"
  vpc_id      = var.vpc_id
}
resource "aws_security_group_rule" "nfs" {
  type                     = "ingress"
  from_port                = 2049
  to_port                  = 2049
  protocol                 = "tcp"
  security_group_id        = aws_security_group.storage.id
  source_security_group_id = aws_security_group.compute.id
}
resource "aws_efs_file_system" "user" {
  for_each         = var.users
  encrypted        = true
  throughput_mode  = "elastic"
  performance_mode = "generalPurpose"
  tags = {
    Owner        = each.key
    StorageScope = "user"
    ManagedBy    = "awsportal"
  }
  lifecycle { prevent_destroy = true }
}
resource "aws_efs_backup_policy" "user" {
  for_each       = var.users
  file_system_id = aws_efs_file_system.user[each.key].id
  backup_policy { status = "ENABLED" }
}
# Preserve the caller's numeric UID/GID. Enforcing a single PosixUser on a
# root-mounted filesystem would map other desktop users to the same identity.
resource "aws_efs_access_point" "home" {
  for_each       = var.users
  file_system_id = aws_efs_file_system.user[each.key].id
  root_directory {
    path = "/home"
    creation_info {
      owner_uid   = 200000 + tonumber(each.key)
      owner_gid   = 200000 + tonumber(each.key)
      permissions = "0700"
    }
  }
  lifecycle { prevent_destroy = true }
}
locals {
  mount_targets = { for pair in setproduct(keys(var.users), keys(var.subnets_by_az)) : "${pair[0]}/${pair[1]}" => { user = pair[0], az = pair[1] } }
}
resource "aws_efs_mount_target" "home" {
  for_each        = local.mount_targets
  file_system_id  = aws_efs_file_system.user[each.value.user].id
  subnet_id       = var.subnets_by_az[each.value.az]
  security_groups = [aws_security_group.storage.id]
}
resource "aws_iam_role" "compute" {
  name_prefix = "${var.name}-compute-"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}
resource "aws_iam_role_policy" "compute" {
  role = aws_iam_role.compute.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat([for uid in keys(var.users) : {
      Effect   = "Allow"
      Action   = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource = aws_efs_file_system.user[uid].arn
      Condition = {
        StringEquals = { "elasticfilesystem:AccessPointArn" = aws_efs_access_point.home[uid].arn }
        Bool         = { "aws:SecureTransport" = "true" }
      }
      }], [{
      Effect   = "Deny"
      Action   = ["elasticfilesystem:ClientRootAccess"]
      Resource = "*"
      }, {
      Effect   = "Allow"
      Action   = ["s3:ListBucket"]
      Resource = var.dataset_bucket_arn
      }, {
      Effect   = "Allow"
      Action   = ["s3:GetObject", "s3:GetObjectVersion"]
      Resource = "${var.dataset_bucket_arn}/*"
    }])
  })
}
resource "aws_efs_file_system_policy" "user" {
  for_each       = var.users
  file_system_id = aws_efs_file_system.user[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { AWS = aws_iam_role.compute.arn }
      Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource  = aws_efs_file_system.user[each.key].arn
      Condition = { StringEquals = { "elasticfilesystem:AccessPointArn" = aws_efs_access_point.home[each.key].arn }, Bool = { "aws:SecureTransport" = "true" } }
      }, {
      Effect    = "Deny"
      Principal = "*"
      Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource  = aws_efs_file_system.user[each.key].arn
      Condition = { ArnNotEquals = { "aws:PrincipalArn" = aws_iam_role.compute.arn } }
      }, {
      Effect    = "Deny"
      Principal = "*"
      Action    = ["elasticfilesystem:ClientRootAccess"]
      Resource  = aws_efs_file_system.user[each.key].arn
      }, {
      Effect    = "Deny"
      Principal = "*"
      Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource  = aws_efs_file_system.user[each.key].arn
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}
resource "aws_iam_instance_profile" "compute" {
  name_prefix = "${var.name}-compute-"
  role        = aws_iam_role.compute.name
}
resource "aws_instance" "fixed" {
  ami                         = var.golden_ami_id
  instance_type               = var.instance_type
  subnet_id                   = values(var.subnets_by_az)[0]
  vpc_security_group_ids      = [aws_security_group.compute.id]
  associate_public_ip_address = false
  iam_instance_profile        = aws_iam_instance_profile.compute.name
  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }
  root_block_device {
    encrypted             = true
    volume_type           = "gp3"
    volume_size           = 100
    delete_on_termination = true
  }
  tags = {
    Name          = var.name
    EnvironmentId = var.environment_id
    GroupId       = var.group_id
    AccessMode    = "Shared"
    ProfileId     = "shared-cpu-v1"
    ManagedBy     = "awsportal"
  }
  depends_on = [aws_efs_mount_target.home, aws_efs_file_system_policy.user]
}
resource "aws_s3_bucket" "usage" {
  bucket = var.metrics_bucket_name
  lifecycle { prevent_destroy = true }
}
resource "aws_s3_bucket_public_access_block" "usage" {
  bucket                  = aws_s3_bucket.usage.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
resource "aws_s3_bucket_versioning" "usage" {
  bucket = aws_s3_bucket.usage.id
  versioning_configuration { status = "Enabled" }
}
resource "aws_s3_bucket_server_side_encryption_configuration" "usage" {
  bucket = aws_s3_bucket.usage.id
  rule {
    apply_server_side_encryption_by_default { sse_algorithm = "AES256" }
  }
}
resource "aws_s3_bucket_policy" "usage" {
  bucket = aws_s3_bucket.usage.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [aws_s3_bucket.usage.arn, "${aws_s3_bucket.usage.arn}/*"], Condition = { Bool = { "aws:SecureTransport" = "false" } } }] })
}
resource "aws_iam_role_policy" "collector" {
  role   = var.portal_role_name
  policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["s3:PutObject"], Resource = "${aws_s3_bucket.usage.arn}/usage/v2/*" }] })
}
output "instance_id" { value = aws_instance.fixed.id }
output "user_storage" { value = { for uid in keys(var.users) : uid => { efs_id = aws_efs_file_system.user[uid].id, access_point_id = aws_efs_access_point.home[uid].id, uid = 200000 + tonumber(uid), gid = 200000 + tonumber(uid) } } }
output "metrics_bucket" { value = aws_s3_bucket.usage.id }

output "compute_security_group_id" { value = aws_security_group.compute.id }
