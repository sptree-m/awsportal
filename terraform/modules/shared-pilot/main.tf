terraform {

  required_version = ">= 1.7.0"
  required_providers {

    aws = {

      source  = "hashicorp/aws"
      version = "~> 5.0"

    }

  }

}

variable "name" {
  type = string
}
variable "vpc_id" {
  type = string
}
variable "subnets_by_az" {
  type = map(string)
}
variable "users" {

  description = "Stable Portal User ID => username. Do not recreate EFS on username changes."
  type        = map(string)
  validation {

    condition     = length(var.users) <= 50 && alltrue([for id in keys(var.users) : can(tonumber(id)) && try(tonumber(id) >= 1 && tonumber(id) <= 1000000, false)])
    error_message = "At most 50 stable positive user IDs are supported."

  }

}
variable "migration_roles" {
  description = "Temporary User ID => dedicated offline migration role ARN. Empty after verified migration. Never use the compute role."
  type        = map(string)
  default     = {}
}
variable "migration_security_group_ids" {
  type    = set(string)
  default = []
}
resource "aws_security_group_rule" "migration_nfs" {
  for_each                 = var.existing_storage_security_group_id == "" ? var.migration_security_group_ids : toset([])
  type                     = "ingress"
  from_port                = 2049
  to_port                  = 2049
  protocol                 = "tcp"
  security_group_id        = local.storage_sg_id
  source_security_group_id = each.value
}
variable "golden_ami_id" {
  type = string
}
variable "instance_type" {

  type    = string
  default = "m7i.xlarge"

}
variable "environment_id" {
  type = string
}
variable "group_id" {
  type = string
}
variable "corporate_cidrs" {
  type = list(string)
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

variable "portal_security_group_id" {
  type    = string
  default = null
}
variable "endpoint_security_group_id" {
  type    = string
  default = null
}
variable "s3_prefix_list_id" {
  type    = string
  default = null
}
variable "metrics_bucket_name" {
  type = string
}
variable "dataset_bucket_arn" {
  type = string
}
variable "portal_role_name" {
  type = string
}

resource "aws_security_group" "compute" {
  count = length(var.existing_compute_security_group_ids) == 0 ? 1 : 0

  name_prefix = "${var.name}-compute-"
  vpc_id      = var.vpc_id
  ingress {

    from_port   = var.dcv_port
    to_port     = var.dcv_port
    protocol    = "tcp"
    cidr_blocks = var.corporate_cidrs

  }
  dynamic "egress" {
    for_each = length(var.approved_https_egress_cidrs) > 0 ? [1] : []
    content {
      from_port   = 443
      to_port     = 443
      protocol    = "tcp"
      cidr_blocks = var.approved_https_egress_cidrs
    }
  }
  dynamic "egress" {
    for_each = length(compact([var.portal_security_group_id, var.endpoint_security_group_id])) > 0 ? [1] : []
    content {
      from_port       = 443
      to_port         = 443
      protocol        = "tcp"
      security_groups = compact([var.portal_security_group_id, var.endpoint_security_group_id])
    }
  }
  dynamic "egress" {
    for_each = var.s3_prefix_list_id != null ? [1] : []
    content {
      from_port       = 443
      to_port         = 443
      protocol        = "tcp"
      prefix_list_ids = [var.s3_prefix_list_id]
    }
  }
  egress {

    from_port       = 2049
    to_port         = 2049
    protocol        = "tcp"
    security_groups = [local.storage_sg_id]

  }

}
resource "aws_security_group_rule" "portal_from_compute" {
  count = var.manage_peer_security_group_rules && var.portal_security_group_id != null ? 1 : 0

  type                     = "ingress"
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  security_group_id        = var.portal_security_group_id
  source_security_group_id = local.compute_sg_ids[0]

}
resource "aws_security_group_rule" "endpoints_from_compute" {
  count = var.manage_peer_security_group_rules && var.endpoint_security_group_id != null ? 1 : 0

  type                     = "ingress"
  from_port                = 443
  to_port                  = 443
  protocol                 = "tcp"
  security_group_id        = var.endpoint_security_group_id
  source_security_group_id = local.compute_sg_ids[0]

}
resource "aws_security_group" "storage" {
  count = var.existing_storage_security_group_id == "" ? 1 : 0

  name_prefix = "${var.name}-efs-"
  vpc_id      = var.vpc_id

}
resource "aws_security_group_rule" "nfs" {
  count = var.existing_storage_security_group_id == "" ? 1 : 0

  type                     = "ingress"
  from_port                = 2049
  to_port                  = 2049
  protocol                 = "tcp"
  security_group_id        = local.storage_sg_id
  source_security_group_id = local.compute_sg_ids[0]

}
resource "aws_efs_file_system" "user" {

  for_each         = local.new_users
  encrypted        = true
  throughput_mode  = "elastic"
  performance_mode = "generalPurpose"
  tags = {

    Owner        = each.key
    StorageScope = "user"
    ManagedBy    = "awsportal"

  }
  lifecycle {
    prevent_destroy = true
  }

}
resource "aws_efs_backup_policy" "user" {

  for_each       = local.new_users
  file_system_id = aws_efs_file_system.user[each.key].id
  backup_policy {
    status = "ENABLED"
  }

}
# Preserve the caller's numeric UID/GID. Enforcing a single PosixUser on a
# root-mounted filesystem would map other desktop users to the same identity.
resource "aws_efs_access_point" "home" {

  for_each       = local.new_users
  file_system_id = aws_efs_file_system.user[each.key].id
  root_directory {

    path = "/home"
    creation_info {

      owner_uid   = 200000 + tonumber(each.key)
      owner_gid   = 200000 + tonumber(each.key)
      permissions = "0700"

    }

  }
  lifecycle {
    prevent_destroy = true
  }

}
locals {

  mount_targets = {
    for pair in setproduct(keys(local.new_users), keys(var.subnets_by_az)) : "${pair[0]}/${pair[1]}" => {
      user = pair[0], az = pair[1]
    }
  }

}
resource "aws_efs_mount_target" "home" {

  for_each        = local.mount_targets
  file_system_id  = aws_efs_file_system.user[each.value.user].id
  subnet_id       = var.subnets_by_az[each.value.az]
  security_groups = [local.storage_sg_id]

}
resource "aws_iam_role" "compute" {
  count = var.existing_compute_role_arn == "" ? 1 : 0

  permissions_boundary = var.permissions_boundary
  name_prefix          = "${var.name}-compute-"
  assume_role_policy = jsonencode({

    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow", Principal = {
        Service = "ec2.amazonaws.com"
      }, Action = "sts:AssumeRole"
    }]

  })

}
resource "aws_iam_role_policy" "compute" {
  count = var.existing_compute_role_arn == "" ? 1 : 0

  role = aws_iam_role.compute[0].id
  policy = jsonencode({

    Version = "2012-10-17"
    Statement = concat([for uid in keys(var.users) : {

      Effect   = "Allow"
      Action   = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource = local.users_storage[uid].efs_arn
      Condition = {

        StringEquals = {
          "elasticfilesystem:AccessPointArn" = local.users_storage[uid].access_point_arn
        }
        Bool = {
          "aws:SecureTransport" = "true"
        }

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

  for_each       = local.new_users
  file_system_id = aws_efs_file_system.user[each.key].id
  lifecycle {
    precondition {
      condition     = !contains(values(var.migration_roles), local.compute_role_arn)
      error_message = "The compute role must never receive migration root access."
    }
  }
  policy = jsonencode({

    Version = "2012-10-17"
    Statement = concat(contains(keys(var.migration_roles), each.key) ? [{
      Effect    = "Allow"
      Principal = { AWS = var.migration_roles[each.key] }
      Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite", "elasticfilesystem:ClientRootAccess"]
      Resource  = local.users_storage[each.key].efs_arn
      Condition = { StringEquals = { "elasticfilesystem:AccessPointArn" = local.users_storage[each.key].access_point_arn }, Bool = { "aws:SecureTransport" = "true" } }
      }] : [], [{

      Effect = "Allow"
      Principal = {
        AWS = local.compute_role_arn
      }
      Action   = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource = local.users_storage[each.key].efs_arn
      Condition = {
        StringEquals = {
          "elasticfilesystem:AccessPointArn" = local.users_storage[each.key].access_point_arn
          }, Bool = {
          "aws:SecureTransport" = "true"
        }
      }

      }, {

      Effect    = "Deny"
      Principal = "*"
      Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource  = local.users_storage[each.key].efs_arn
      Condition = {
        ArnNotEquals = {
          "aws:PrincipalArn" = concat([local.compute_role_arn], contains(keys(var.migration_roles), each.key) ? [var.migration_roles[each.key]] : [])
        }
      }

      }, {

      Effect    = "Deny"
      Principal = "*"
      Action    = ["elasticfilesystem:ClientRootAccess"]
      Resource  = local.users_storage[each.key].efs_arn
      Condition = { ArnNotEquals = { "aws:PrincipalArn" = lookup(var.migration_roles, each.key, "arn:aws:iam::000000000000:role/no-migration") } }

      }, {

      Effect    = "Deny"
      Principal = "*"
      Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"]
      Resource  = local.users_storage[each.key].efs_arn
      Condition = {
        Bool = {
          "aws:SecureTransport" = "false"
        }
      }

    }])

  })

}
resource "aws_iam_instance_profile" "compute" {
  count = var.existing_compute_role_arn == "" ? 1 : 0

  name_prefix = "${var.name}-compute-"
  role        = aws_iam_role.compute[0].name

}
resource "aws_instance" "fixed" {

  lifecycle {
    precondition {
      condition     = alltrue([for az, sub in data.aws_subnet.approved : sub.vpc_id == var.vpc_id && sub.availability_zone == az]) && alltrue([for sg in data.aws_security_group.provided : sg.vpc_id == var.vpc_id])
      error_message = "Subnets/AZs and provided SGs must match the approved VPC."
    }
    precondition {
      condition     = alltrue([for uid, fs in data.aws_efs_file_system.provided_user : fs.encrypted && data.aws_efs_access_point.provided_user[uid].file_system_id == fs.id && length(data.aws_efs_access_point.provided_user[uid].posix_user) == 0 && data.aws_efs_access_point.provided_user[uid].root_directory[0].creation_info[0].owner_uid == 200000 + tonumber(uid) && data.aws_efs_access_point.provided_user[uid].root_directory[0].creation_info[0].permissions == "0700"])
      error_message = "Provided HOME EFS/AP must be encrypted, preserve numeric UID, use 0700 and not force a shared PosixUser."
    }
    precondition {
      condition     = alltrue([for uid in keys(var.existing_user_storage) : contains(keys(var.users), uid)]) && (var.existing_compute_role_arn == "" || (var.existing_compute_instance_profile_name != "" && var.existing_compute_instance_profile_arn != ""))
      error_message = "Provided storage users and instance profile must be explicitly approved."
    }
  }
  ami                         = var.golden_ami_id
  private_ip                  = var.fixed_private_ip
  instance_type               = var.instance_type
  subnet_id                   = values(var.subnets_by_az)[0]
  vpc_security_group_ids      = local.compute_sg_ids
  associate_public_ip_address = false
  iam_instance_profile        = local.compute_profile_name
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
  ebs_block_device {
    device_name           = "/dev/sdf"
    encrypted             = true
    volume_type           = "gp3"
    volume_size           = 1024
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
  count = var.existing_metrics_bucket_name == "" ? 1 : 0

  bucket = var.metrics_bucket_name
  lifecycle {
    prevent_destroy = true
  }

}
resource "aws_s3_bucket_public_access_block" "usage" {
  count = var.existing_metrics_bucket_name == "" ? 1 : 0

  bucket                  = local.metrics_bucket
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true

}
resource "aws_s3_bucket_versioning" "usage" {
  count = var.existing_metrics_bucket_name == "" ? 1 : 0

  bucket = local.metrics_bucket
  versioning_configuration {
    status = "Enabled"
  }

}
resource "aws_s3_bucket_server_side_encryption_configuration" "usage" {
  count = var.existing_metrics_bucket_name == "" ? 1 : 0

  bucket = local.metrics_bucket
  rule {

    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }

  }

}
resource "aws_s3_bucket_policy" "usage" {
  count = var.existing_metrics_bucket_name == "" ? 1 : 0

  bucket = local.metrics_bucket
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [local.metrics_bucket_arn, "${local.metrics_bucket_arn}/*"], Condition = {
        Bool = {
          "aws:SecureTransport" = "false"
        }
      }
    }]
  })

}
resource "aws_iam_role_policy" "collector" {
  count = var.manage_portal_policies ? 1 : 0

  role = var.portal_role_name
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Action = ["s3:PutObject"], Resource = "${local.metrics_bucket_arn}/usage/*"
    }]
  })

}
output "instance_id" {
  value = aws_instance.fixed.id
}
output "user_storage" {
  value = {
    for uid in keys(var.users) : uid => {
      efs_id = local.users_storage[uid].efs_id, access_point_id = local.users_storage[uid].access_point_id, uid = 200000 + tonumber(uid), gid = 200000 + tonumber(uid)
    }
  }
}
output "metrics_bucket" {
  value = local.metrics_bucket
}

output "compute_security_group_id" {
  value = local.compute_sg_ids[0]
}

resource "aws_s3_bucket_lifecycle_configuration" "usage" {
  count = var.existing_metrics_bucket_name == "" ? 1 : 0

  bucket = local.metrics_bucket
  rule {

    id     = "raw-thirteen-months"
    status = "Enabled"
    filter {
      prefix = "usage/"
    }
    expiration {
      days = 396
    }
    noncurrent_version_expiration {
      noncurrent_days = 396
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }

  }

}

resource "aws_efs_file_system" "group" {
  count = var.existing_group_storage == null ? 1 : 0

  encrypted       = true
  throughput_mode = "elastic"
  tags = {
    GroupId = var.group_id, StorageScope = "group", ManagedBy = "awsportal"
  }
  lifecycle {
    prevent_destroy = true
  }

}
resource "aws_efs_access_point" "group" {
  count = var.existing_group_storage == null ? 1 : 0

  file_system_id = aws_efs_file_system.group[0].id
  root_directory {

    path = "/group"
    creation_info {
      owner_uid   = 2000000 + tonumber(var.group_id)
      owner_gid   = 2000000 + tonumber(var.group_id)
      permissions = "2770"
    }

  }
  lifecycle {
    prevent_destroy = true
  }

}
resource "aws_efs_mount_target" "group" {

  for_each        = var.existing_group_storage == null ? var.subnets_by_az : {}
  file_system_id  = aws_efs_file_system.group[0].id
  subnet_id       = each.value
  security_groups = [local.storage_sg_id]

}
resource "aws_efs_backup_policy" "group" {
  count = var.existing_group_storage == null ? 1 : 0

  file_system_id = aws_efs_file_system.group[0].id
  backup_policy {
    status = "ENABLED"
  }

}
resource "aws_iam_role_policy" "group" {
  count = var.existing_compute_role_arn == "" ? 1 : 0

  role = aws_iam_role.compute[0].id
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Action = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"], Resource = local.group_storage.efs_arn, Condition = {
        StringEquals = {
          "elasticfilesystem:AccessPointArn" = local.group_storage.access_point_arn
          }, Bool = {
          "aws:SecureTransport" = "true"
        }
      }
    }]
  })

}
resource "aws_efs_file_system_policy" "group" {
  count = var.existing_group_storage == null ? 1 : 0

  file_system_id = aws_efs_file_system.group[0].id
  policy = jsonencode({
    Version = "2012-10-17", Statement = [
      {
        Effect = "Allow", Principal = {
          AWS = local.compute_role_arn
          }, Action = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"], Resource = local.group_storage.efs_arn, Condition = {
          StringEquals = {
            "elasticfilesystem:AccessPointArn" = local.group_storage.access_point_arn
            }, Bool = {
            "aws:SecureTransport" = "true"
          }
        }
      },
      {
        Effect = "Deny", Principal = "*", Action = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"], Resource = local.group_storage.efs_arn, Condition = {
          ArnNotEquals = {
            "aws:PrincipalArn" = local.compute_role_arn
          }
        }
      },
      {
        Effect = "Deny", Principal = "*", Action = ["elasticfilesystem:ClientRootAccess"], Resource = local.group_storage.efs_arn
      },
      {
        Effect = "Deny", Principal = "*", Action = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite"], Resource = local.group_storage.efs_arn, Condition = {
          Bool = {
            "aws:SecureTransport" = "false"
          }
        }
      }
    ]
  })

}
output "group_storage" {
  value = {
    efs_id = local.group_storage.efs_id, access_point_id = local.group_storage.access_point_id, gid = 2000000 + tonumber(var.group_id)
  }
}

resource "aws_iam_role_policy" "portal_storage_meter" {
  count  = var.manage_portal_policies ? 1 : 0
  role   = var.portal_role_name
  policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["elasticfilesystem:DescribeFileSystems", "ec2:DescribeVolumes"], Resource = "*" }] })
}

output "compute_role_arn" { value = local.compute_role_arn }
output "compute_instance_profile_arn" { value = var.existing_compute_role_arn != "" ? var.existing_compute_instance_profile_arn : aws_iam_instance_profile.compute[0].arn }

variable "existing_compute_security_group_ids" {
  type    = list(string)
  default = []
}
variable "existing_storage_security_group_id" {
  type    = string
  default = ""
}
variable "existing_compute_role_arn" {
  type    = string
  default = ""
}
variable "existing_compute_instance_profile_name" {
  type    = string
  default = ""
}
variable "existing_compute_instance_profile_arn" {
  type    = string
  default = ""
}
variable "permissions_boundary" {
  type    = string
  default = null
}
variable "manage_peer_security_group_rules" {
  type    = bool
  default = true
}
variable "manage_portal_policies" {
  type    = bool
  default = true
}
variable "approved_https_egress_cidrs" {
  type    = list(string)
  default = []
}
variable "fixed_private_ip" {
  type    = string
  default = null
}
variable "existing_metrics_bucket_name" {
  type    = string
  default = ""
}
variable "existing_user_storage" {
  type    = map(object({ efs_id = string, efs_arn = string, access_point_id = string, access_point_arn = string }))
  default = {}
}
variable "existing_group_storage" {
  type    = object({ efs_id = string, efs_arn = string, access_point_id = string, access_point_arn = string })
  default = null
}
locals {
  new_users            = { for uid, name in var.users : uid => name if !contains(keys(var.existing_user_storage), uid) }
  compute_sg_ids       = length(var.existing_compute_security_group_ids) > 0 ? var.existing_compute_security_group_ids : [aws_security_group.compute[0].id]
  storage_sg_id        = var.existing_storage_security_group_id != "" ? var.existing_storage_security_group_id : aws_security_group.storage[0].id
  compute_role_arn     = var.existing_compute_role_arn != "" ? var.existing_compute_role_arn : aws_iam_role.compute[0].arn
  compute_profile_name = var.existing_compute_role_arn != "" ? var.existing_compute_instance_profile_name : aws_iam_instance_profile.compute[0].name
  users_storage        = merge({ for uid in keys(local.new_users) : uid => { efs_id = aws_efs_file_system.user[uid].id, efs_arn = aws_efs_file_system.user[uid].arn, access_point_id = aws_efs_access_point.home[uid].id, access_point_arn = aws_efs_access_point.home[uid].arn } }, var.existing_user_storage)
  group_storage        = var.existing_group_storage != null ? var.existing_group_storage : { efs_id = aws_efs_file_system.group[0].id, efs_arn = aws_efs_file_system.group[0].arn, access_point_id = aws_efs_access_point.group[0].id, access_point_arn = aws_efs_access_point.group[0].arn }
  metrics_bucket       = var.existing_metrics_bucket_name != "" ? var.existing_metrics_bucket_name : aws_s3_bucket.usage[0].id
  metrics_bucket_arn   = "arn:aws:s3:::${local.metrics_bucket}"
}

check "provided_role" {
  assert {
    condition     = var.existing_compute_role_arn == "" || (var.existing_compute_instance_profile_name != "" && var.existing_compute_instance_profile_arn != "")
    error_message = "Existing role requires its approved instance profile name and ARN."
  }
}

moved {
  from = aws_security_group.compute
  to   = aws_security_group.compute[0]
}
moved {
  from = aws_security_group_rule.portal_from_compute
  to   = aws_security_group_rule.portal_from_compute[0]
}
moved {
  from = aws_security_group_rule.endpoints_from_compute
  to   = aws_security_group_rule.endpoints_from_compute[0]
}
moved {
  from = aws_security_group.storage
  to   = aws_security_group.storage[0]
}
moved {
  from = aws_security_group_rule.nfs
  to   = aws_security_group_rule.nfs[0]
}
moved {
  from = aws_iam_role.compute
  to   = aws_iam_role.compute[0]
}
moved {
  from = aws_iam_role_policy.compute
  to   = aws_iam_role_policy.compute[0]
}
moved {
  from = aws_iam_instance_profile.compute
  to   = aws_iam_instance_profile.compute[0]
}
moved {
  from = aws_s3_bucket.usage
  to   = aws_s3_bucket.usage[0]
}
moved {
  from = aws_s3_bucket_public_access_block.usage
  to   = aws_s3_bucket_public_access_block.usage[0]
}
moved {
  from = aws_s3_bucket_versioning.usage
  to   = aws_s3_bucket_versioning.usage[0]
}
moved {
  from = aws_s3_bucket_server_side_encryption_configuration.usage
  to   = aws_s3_bucket_server_side_encryption_configuration.usage[0]
}
moved {
  from = aws_s3_bucket_policy.usage
  to   = aws_s3_bucket_policy.usage[0]
}
moved {
  from = aws_iam_role_policy.collector
  to   = aws_iam_role_policy.collector[0]
}
moved {
  from = aws_s3_bucket_lifecycle_configuration.usage
  to   = aws_s3_bucket_lifecycle_configuration.usage[0]
}
moved {
  from = aws_efs_file_system.group
  to   = aws_efs_file_system.group[0]
}
moved {
  from = aws_efs_access_point.group
  to   = aws_efs_access_point.group[0]
}
moved {
  from = aws_efs_backup_policy.group
  to   = aws_efs_backup_policy.group[0]
}
moved {
  from = aws_iam_role_policy.group
  to   = aws_iam_role_policy.group[0]
}
moved {
  from = aws_efs_file_system_policy.group
  to   = aws_efs_file_system_policy.group[0]
}
moved {
  from = aws_iam_role_policy.portal_storage_meter
  to   = aws_iam_role_policy.portal_storage_meter[0]
}

data "aws_subnet" "approved" {
  for_each = var.subnets_by_az
  id       = each.value
}
data "aws_security_group" "provided" {
  for_each = toset(concat(var.existing_compute_security_group_ids, var.existing_storage_security_group_id != "" ? [var.existing_storage_security_group_id] : []))
  id       = each.value
}
data "aws_efs_file_system" "provided_user" {
  for_each       = var.existing_user_storage
  file_system_id = each.value.efs_id
}
data "aws_efs_access_point" "provided_user" {
  for_each        = var.existing_user_storage
  access_point_id = each.value.access_point_id
}
