variable "aws_region" {
  type    = string
  default = "ap-northeast-1"
}
variable "vpc_id" { type = string }
variable "private_subnet_id" { type = string }
variable "corporate_cidrs" { type = list(string) }
variable "portal_ami_id" { type = string }
variable "managed_instance_arns" { type = list(string) }
variable "portal_instance_type" {
  type    = string
  default = "t4g.micro"
}
variable "managed_egress_instance_ids" {
  type    = set(string)
  default = []
}

variable "enable_mirror_access" {
  type    = bool
  default = false
}
