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

  bucket = var.bucket_name
  lifecycle {
    prevent_destroy = true
  }

}
resource "aws_s3_bucket_versioning" "cur" {

  bucket = aws_s3_bucket.cur.id
  versioning_configuration {
    status = "Enabled"
  }

}
resource "aws_s3_bucket_public_access_block" "cur" {

  bucket                  = aws_s3_bucket.cur.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true

}
resource "aws_s3_bucket_server_side_encryption_configuration" "cur" {

  bucket = aws_s3_bucket.cur.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }

}
resource "aws_s3_bucket_policy" "cur" {

  bucket = aws_s3_bucket.cur.id
  policy = jsonencode({
    Version = "2012-10-17", Statement = [
      {
        Effect = "Allow", Principal = {
          Service = ["bcm-data-exports.amazonaws.com", "billingreports.amazonaws.com"]
          }, Action = ["s3:GetBucketAcl", "s3:GetBucketPolicy"], Resource = aws_s3_bucket.cur.arn, Condition = {
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
          }, Action = ["s3:PutObject"], Resource = "${aws_s3_bucket.cur.arn}/cur/*", Condition = {
          StringEquals = {
            "aws:SourceAccount" = data.aws_caller_identity.current.account_id
            }, ArnLike = {
            "aws:SourceArn" = "arn:aws:bcm-data-exports:us-east-1:${data.aws_caller_identity.current.account_id}:export/*"
          }
        }
      },
      {
        Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [aws_s3_bucket.cur.arn, "${aws_s3_bucket.cur.arn}/*"], Condition = {
          Bool = {
            "aws:SecureTransport" = "false"
          }
        }
      }
    ]
  })

}
resource "aws_bcmdataexports_export" "cur" {

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

        s3_bucket = aws_s3_bucket.cur.bucket
        s3_prefix = "cur/"
        s3_region = data.aws_region.current.name
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

  role = var.portal_role_name
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Action = ["s3:GetObject", "s3:GetObjectVersion"], Resource = "${aws_s3_bucket.cur.arn}/cur/*"
      }, {
      Effect = "Allow", Action = ["s3:ListBucket", "s3:ListBucketVersions"], Resource = aws_s3_bucket.cur.arn, Condition = {
        StringLike = {
          "s3:prefix" = "cur/*"
        }
      }
    }]
  })

}
output "bucket" {
  value = aws_s3_bucket.cur.bucket
}
output "export_arn" {
  value = aws_bcmdataexports_export.cur.export_arn
}
