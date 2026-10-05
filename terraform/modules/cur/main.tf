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
variable "bucket_name" {
  type = string
}
variable "portal_role_name" {
  type = string
}
data "aws_caller_identity" "current" {

}
data "aws_region" "current" {

}
resource "aws_s3_bucket" "cur" {
  count = var.use_existing_bucket ? 0 : 1

  bucket = var.bucket_name
  lifecycle {
    prevent_destroy = true
  }

}
resource "aws_s3_bucket_versioning" "cur" {
  count = var.use_existing_bucket ? 0 : 1

  bucket = var.bucket_name
  versioning_configuration {
    status = "Enabled"
  }

}
resource "aws_s3_bucket_public_access_block" "cur" {
  count = var.use_existing_bucket ? 0 : 1

  bucket                  = var.bucket_name
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true

}
resource "aws_s3_bucket_server_side_encryption_configuration" "cur" {
  count = var.use_existing_bucket ? 0 : 1

  bucket = var.bucket_name
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }

}
resource "aws_s3_bucket_policy" "cur" {
  count = var.use_existing_bucket ? 0 : 1

  bucket = var.bucket_name
  policy = jsonencode({
    Version = "2012-10-17", Statement = [
      {
        Effect = "Allow", Principal = {
          Service = ["bcm-data-exports.amazonaws.com", "billingreports.amazonaws.com"]
          }, Action = ["s3:GetBucketAcl", "s3:GetBucketPolicy"], Resource = local.bucket_arn, Condition = {
          StringEquals = {
            "aws:SourceAccount" = data.aws_caller_identity.current.account_id
            }, ArnLike = {
            "aws:SourceArn" = "arn:aws:bcm-data-exports:us-east-1:${data.aws_caller_identity.current.account_id}:export/*"
          }
        }
      },
      {
        Effect = "Allow", Principal = {
          Service = ["bcm-data-exports.amazonaws.com", "billingreports.amazonaws.com"]
          }, Action = ["s3:PutObject"], Resource = "${local.bucket_arn}/${var.prefix}*", Condition = {
          StringEquals = {
            "aws:SourceAccount" = data.aws_caller_identity.current.account_id
            }, ArnLike = {
            "aws:SourceArn" = "arn:aws:bcm-data-exports:us-east-1:${data.aws_caller_identity.current.account_id}:export/*"
          }
        }
      },
      {
        Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [local.bucket_arn, "${local.bucket_arn}/*"], Condition = {
          Bool = {
            "aws:SecureTransport" = "false"
          }
        }
      }
    ]
  })

}
resource "aws_bcmdataexports_export" "cur" {
  count = var.create_export ? 1 : 0

  export {

    name = var.name
    data_query {

      query_statement = "SELECT identity_line_item_id, line_item_usage_account_id, line_item_currency_code, line_item_line_item_type, line_item_resource_id, line_item_usage_start_date, line_item_usage_end_date, line_item_unblended_cost, line_item_net_unblended_cost FROM COST_AND_USAGE_REPORT"
      table_configurations = {
        COST_AND_USAGE_REPORT = {
          TIME_GRANULARITY = "HOURLY", INCLUDE_RESOURCES = "TRUE", INCLUDE_MANUAL_DISCOUNT_COMPATIBILITY = "FALSE", INCLUDE_SPLIT_COST_ALLOCATION_DATA = "FALSE"
        }
      }

    }
    destination_configurations {

      s3_destination {

        s3_bucket = var.bucket_name
        s3_prefix = var.prefix
        s3_region = var.use_existing_bucket && var.existing_bucket_region != null ? var.existing_bucket_region : data.aws_region.current.name
        s3_output_configurations {
          overwrite   = "OVERWRITE_REPORT"
          format      = "TEXT_OR_CSV"
          compression = "GZIP"
          output_type = "CUSTOM"

        }

      }

    }
    refresh_cadence {
      frequency = "SYNCHRONOUS"
    }

  }
  depends_on = [aws_s3_bucket_policy.cur, aws_s3_bucket_versioning.cur]

}
resource "aws_iam_role_policy" "portal" {
  count = var.manage_portal_policy ? 1 : 0

  role = var.portal_role_name
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Action = ["s3:GetObject", "s3:GetObjectVersion"], Resource = "${local.bucket_arn}/${var.prefix}*"
      }, {
      Effect = "Allow", Action = ["s3:ListBucket", "s3:ListBucketVersions"], Resource = local.bucket_arn, Condition = {
        StringLike = {
          "s3:prefix" = "${var.prefix}*"
        }
      }
    }]
  })

}
output "bucket" {
  value = var.bucket_name
}
output "export_arn" {
  value = var.create_export ? aws_bcmdataexports_export.cur[0].export[0].export_arn : var.existing_export_arn
}

variable "use_existing_bucket" {
  type    = bool
  default = false
}
variable "create_export" {
  type    = bool
  default = true
}
variable "manage_portal_policy" {
  type    = bool
  default = true
}
variable "existing_export_arn" {
  type    = string
  default = ""
}
variable "prefix" {
  type    = string
  default = "cur/"
}
locals { bucket_arn = "arn:aws:s3:::${var.bucket_name}" }

moved {
  from = aws_s3_bucket.cur
  to   = aws_s3_bucket.cur[0]
}
moved {
  from = aws_s3_bucket_versioning.cur
  to   = aws_s3_bucket_versioning.cur[0]
}
moved {
  from = aws_s3_bucket_public_access_block.cur
  to   = aws_s3_bucket_public_access_block.cur[0]
}
moved {
  from = aws_s3_bucket_server_side_encryption_configuration.cur
  to   = aws_s3_bucket_server_side_encryption_configuration.cur[0]
}
moved {
  from = aws_s3_bucket_policy.cur
  to   = aws_s3_bucket_policy.cur[0]
}
moved {
  from = aws_bcmdataexports_export.cur
  to   = aws_bcmdataexports_export.cur[0]
}
moved {
  from = aws_iam_role_policy.portal
  to   = aws_iam_role_policy.portal[0]
}

variable "existing_bucket_region" {
  description = "Region of the provided CUR bucket; defaults to the provider region."
  type        = string
  default     = null
}
