terraform {

  required_version = ">= 1.7.0"
  required_providers {
    aws = {
      source = "hashicorp/aws", version = "~> 5.0"
    }
    archive = {
      source = "hashicorp/archive", version = "~> 2.4"
    }
  }

}
variable "name" {
  type = string
}
variable "approved_pools" {

  description = "Pool ID to immutable approved launch template ID/version; use separate module for Windows."
  type = map(object({
    launch_template_id = string, launch_template_version = string, ami_id = string, ami_checksum = string,
    network            = optional(object({ vpc_id = string, subnet_id = string, security_group_ids = list(string), allowed_ipv4_cidrs = list(string), route_table_id = string, transit_gateway_id = optional(string, "") }))
  }))
  validation {

    condition     = alltrue([for p in values(var.approved_pools) : can(regex("^[0-9]+$", p.launch_template_version))])
    error_message = "Pin a numeric launch template version; Latest/Default are forbidden."

  }

}
variable "resource_class" {

  type = string
  validation {

    condition     = contains(["shared", "windows-import"], var.resource_class)
    error_message = "Separate Shared and Windows resource classes required."

  }

}
variable "credential_prefix" {
  type = string
}
variable "kms_key_arn" {
  type = string
}
variable "portal_role_name" {
  type = string
}
data "aws_caller_identity" "current" {

}
data "aws_region" "current" {

}
data "archive_file" "worker" {

  type        = "zip"
  source_file = "${path.module}/../../../workflows/cloud_control.py"
  output_path = "${path.module}/worker.zip"

}
resource "aws_iam_role" "worker" {
  count                = var.worker_role_arn == "" ? 1 : 0
  permissions_boundary = var.permissions_boundary

  name = "${var.name}-cloud-worker"
  assume_role_policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Principal = {
        Service = "lambda.amazonaws.com"
      }, Action = "sts:AssumeRole"
    }]
  })

}
variable "approved_instance_role_arns" {
  type = list(string)
}
resource "aws_iam_role_policy" "worker" {
  count = var.worker_role_arn == "" ? 1 : 0

  role   = aws_iam_role.worker[0].id
  policy = local.worker_policy
}
resource "aws_lambda_function" "worker" {

  function_name                  = "${var.name}-cloud-worker"
  role                           = var.worker_role_arn != "" ? var.worker_role_arn : aws_iam_role.worker[0].arn
  filename                       = data.archive_file.worker.output_path
  source_code_hash               = data.archive_file.worker.output_base64sha256
  runtime                        = "python3.12"
  publish                        = true
  handler                        = "cloud_control.handler"
  timeout                        = 60
  reserved_concurrent_executions = 1
  environment {
    variables = {
      APPROVED_POOLS = jsonencode(var.approved_pools), RESOURCE_CLASS = var.resource_class, CREDENTIAL_PREFIX = var.credential_prefix, KMS_KEY_ID = var.kms_key_arn
    }
  }

}
resource "aws_iam_role" "workflow" {
  count                = var.workflow_role_arn == "" ? 1 : 0
  permissions_boundary = var.permissions_boundary

  name = "${var.name}-workflow"
  assume_role_policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Principal = {
        Service = "states.amazonaws.com"
      }, Action = "sts:AssumeRole"
    }]
  })

}
resource "aws_iam_role_policy" "workflow" {
  count = var.workflow_role_arn == "" ? 1 : 0

  role = aws_iam_role.workflow[0].id
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Action = ["lambda:InvokeFunction"], Resource = "${aws_lambda_function.worker.arn}:*"
    }]
  })

}
resource "aws_sfn_state_machine" "cloud" {

  for_each = toset(var.resource_class == "shared" ? ["provision", "terminate", "performance"] : ["provision", "terminate"])
  name     = "${var.name}-${each.key}"
  type     = "STANDARD"
  role_arn = var.workflow_role_arn != "" ? var.workflow_role_arn : aws_iam_role.workflow[0].arn
  definition = jsonencode({
    StartAt = "Act", TimeoutSeconds = each.key == "performance" ? 86400 : 1800, States = {

      Act = {
        Type = "Task", Resource = aws_lambda_function.worker.qualified_arn, ResultPath = "$.result", Next = "Pending", Retry = [{
          ErrorEquals = ["States.TaskFailed"], IntervalSeconds = 5, MaxAttempts = 6, BackoffRate = 2
        }]
      },
      Pending = {
        Type = "Choice", Choices = [{
          Variable  = "$.result.pending", BooleanEquals = true, Next = "Wait"
        }], Default = "Done"
      },
      Wait = {
        Type = "Wait", Seconds = 10, Next = "Act"
      },
      Done = {
        Type = "Pass", InputPath = "$.result", End = true
      }

    }
  })

}
resource "aws_iam_role_policy" "portal" {
  count = var.manage_portal_policy ? 1 : 0

  role = var.portal_role_name
  policy = jsonencode({
    Version = "2012-10-17", Statement = [
      {
        Effect = "Allow", Action = ["states:StartExecution"], Resource = [for s in aws_sfn_state_machine.cloud : s.arn]
      },
      {
        Effect = "Allow", Action = ["states:DescribeExecution"], Resource = [for s in aws_sfn_state_machine.cloud : replace(s.arn, ":stateMachine:", ":execution:") + ":*"]
      }
    ]
  })

}
output "workflow_arns" {
  value = {
    for k, s in aws_sfn_state_machine.cloud : k => s.arn
  }
}
variable "config_parameter_arns" {
  type    = list(string)
  default = []
}
resource "aws_iam_role_policy" "node_bootstrap" {

  for_each = var.manage_node_policies ? toset(var.approved_instance_role_arns) : toset([])
  role     = element(reverse(split("/", each.value)), 0)
  name     = "${var.name}-root-bootstrap"
  policy = jsonencode({
    Version = "2012-10-17", Statement = [
      {
        Effect = "Allow", Action = ["ssm:GetParameter"], Resource = concat(var.config_parameter_arns, ["arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:parameter${var.credential_prefix}/*"])
      },
      {
        Effect = "Allow", Action = ["kms:Decrypt"], Resource = var.kms_key_arn, Condition = {
          StringEquals = {
            "kms:ViaService" = "ssm.${data.aws_region.current.name}.amazonaws.com"
          }
        }
      },
      {
        Effect = "Allow", Action = ["ec2:DescribeInstances"], Resource = "*"
      }
    ]
  })

}

variable "worker_role_arn" {
  type    = string
  default = ""
}
variable "workflow_role_arn" {
  type    = string
  default = ""
}
variable "permissions_boundary" {
  type    = string
  default = null
}
variable "manage_portal_policy" {
  type    = bool
  default = true
}
variable "manage_node_policies" {
  type    = bool
  default = true
}


moved {
  from = aws_iam_role.worker
  to   = aws_iam_role.worker[0]
}
moved {
  from = aws_iam_role_policy.worker
  to   = aws_iam_role_policy.worker[0]
}
moved {
  from = aws_iam_role.workflow
  to   = aws_iam_role.workflow[0]
}
moved {
  from = aws_iam_role_policy.workflow
  to   = aws_iam_role_policy.workflow[0]
}
moved {
  from = aws_iam_role_policy.portal
  to   = aws_iam_role_policy.portal[0]
}

locals {
  worker_policy = jsonencode({
    Version = "2012-10-17", Statement = concat([
      {
        Effect = "Allow", Action = ["ec2:RunInstances"], Resource = "*", Condition = {
          ArnEquals = {
            "ec2:LaunchTemplate" = [for p in values(var.approved_pools) : "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:launch-template/${p.launch_template_id}"]
            }, Bool = {
            "ec2:IsLaunchTemplateResource" = "true"
          }
        }
      },
      {
        Effect = "Allow", Action = ["ec2:CreateTags"], Resource = "*", Condition = {
          StringEquals = {
            "ec2:CreateAction" = "RunInstances"
          }
        }
      },
      {
        Effect = "Allow", Action = ["ec2:TerminateInstances"], Resource = "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:instance/*", Condition = {
          StringEquals = {
            "ec2:ResourceTag/awsportal:managed" = var.resource_class
          }
        }
      },
      {
        Effect = "Allow", Action = ["ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeLaunchTemplateVersions", "ec2:DescribeImages", "ec2:DescribeSubnets", "ec2:DescribeRouteTables", "ec2:DescribeSecurityGroups", "ec2:DescribeNetworkInterfaces", "ec2:DescribeVolumesModifications"], Resource = "*"
      },
      {
        Effect = "Allow", Action = ["ec2:ModifyVolume"], Resource = "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:volume/*", Condition = { StringEquals = { "ec2:ResourceTag/awsportal:managed" = var.resource_class } }
      },
      {
        Effect = "Allow", Action = ["iam:PassRole"], Resource = var.approved_instance_role_arns, Condition = {
          StringEquals = {
            "iam:PassedToService" = "ec2.amazonaws.com"
          }
        }
      },
      {
        Effect = "Allow", Action = ["ssm:PutParameter", "ssm:GetParameter", "ssm:DeleteParameter", "ssm:AddTagsToResource"], Resource = "arn:aws:ssm:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:parameter${var.credential_prefix}/*"
      },
      {
        Effect = "Allow", Action = ["kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"], Resource = var.kms_key_arn
      }
      ], flatten([for p in values(var.approved_pools) : p.network == null ? [] : [
        {
          Effect    = "Allow", Action = ["ec2:RunInstances"],
          Resource  = concat(["arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:subnet/${p.network.subnet_id}"], [for sg in p.network.security_group_ids : "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:security-group/${sg}"]),
          Condition = { ArnEquals = { "ec2:LaunchTemplate" = "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:launch-template/${p.launch_template_id}" } }
        },
        {
          Effect    = "Allow", Action = ["ec2:RunInstances"],
          Resource  = "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:network-interface/*",
          Condition = { ArnEquals = { "ec2:LaunchTemplate" = "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:launch-template/${p.launch_template_id}", "ec2:Subnet" = "arn:aws:ec2:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:subnet/${p.network.subnet_id}" }, Bool = { "ec2:AssociatePublicIpAddress" = "false" } }
        }
    ]]))
  })

}
output "worker_policy_json" { value = local.worker_policy }
