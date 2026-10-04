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
    launch_template_id = string, launch_template_version = string, ami_id = string, ami_checksum = string
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

  role = aws_iam_role.worker.id
  policy = jsonencode({
    Version = "2012-10-17", Statement = [
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
        Effect = "Allow", Action = ["ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeLaunchTemplateVersions", "ec2:DescribeImages"], Resource = "*"
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
    ]
  })

}
resource "aws_lambda_function" "worker" {

  function_name    = "${var.name}-cloud-worker"
  role             = aws_iam_role.worker.arn
  filename         = data.archive_file.worker.output_path
  source_code_hash = data.archive_file.worker.output_base64sha256
  runtime          = "python3.12"
  publish          = true
  handler          = "cloud_control.handler"
  timeout          = 60
  environment {
    variables = {
      APPROVED_POOLS = jsonencode(var.approved_pools), RESOURCE_CLASS = var.resource_class, CREDENTIAL_PREFIX = var.credential_prefix, KMS_KEY_ID = var.kms_key_arn
    }
  }

}
resource "aws_iam_role" "workflow" {

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

  role = aws_iam_role.workflow.id
  policy = jsonencode({
    Version = "2012-10-17", Statement = [{
      Effect = "Allow", Action = ["lambda:InvokeFunction"], Resource = "${aws_lambda_function.worker.arn}:*"
    }]
  })

}
resource "aws_sfn_state_machine" "cloud" {

  for_each = toset(["provision", "terminate"])
  name     = "${var.name}-${each.key}"
  type     = "STANDARD"
  role_arn = aws_iam_role.workflow.arn
  definition = jsonencode({
    StartAt = "Act", TimeoutSeconds = 1800, States = {

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

  for_each = toset(var.approved_instance_role_arns)
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
