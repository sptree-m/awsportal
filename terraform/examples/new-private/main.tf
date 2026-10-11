provider "aws" { region = "ap-northeast-1" }
module "network" {
  source                      = "../../modules/org-network"
  name                        = "awsportal"
  vpc_cidr                    = "10.42.0.0/16"
  subnets                     = { a = { cidr = "10.42.1.0/24", availability_zone = "ap-northeast-1a" } }
  interface_endpoint_services = ["ssm", "ssmmessages", "kms", "ec2", "states", "logs"]
  endpoint_client_cidrs       = ["10.42.0.0/16"]
  create_s3_gateway_endpoint  = true
  s3_endpoint_policy          = "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":\"*\",\"Action\":[\"s3:GetObject\",\"s3:GetObjectVersion\",\"s3:PutObject\",\"s3:ListBucket\"],\"Resource\":[\"arn:aws:s3:::REPLACE_APPROVED_BUCKET\",\"arn:aws:s3:::REPLACE_APPROVED_BUCKET/*\"]}]}"
}
output "network" { value = module.network }
