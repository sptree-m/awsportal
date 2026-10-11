mock_provider "aws" {
  mock_data "aws_ami" { defaults = { root_device_name = "/dev/xvda", platform = "linux" } }
  mock_data "aws_subnet" { defaults = { vpc_id = "vpc-test", cidr_block = "10.42.1.0/24", arn = "arn:aws:ec2:ap-northeast-1:999999999999:subnet/subnet-test" } }
  mock_data "aws_route_table" { defaults = { vpc_id = "vpc-test" } }
  mock_data "aws_security_group" { defaults = { vpc_id = "vpc-test", arn = "arn:aws:ec2:ap-northeast-1:999999999999:security-group/sg-test" } }
}
variables {
  name                  = "test"
  ami_id                = "ami-test"
  ami_checksum          = "test"
  resource_class        = "shared"
  subnet_id             = "subnet-test"
  route_table_id        = "rtb-test"
  security_group_ids    = ["sg-test"]
  instance_profile_name = "provided"
  config_parameter      = "/awsportal/shared/config"
  allowed_ipv4_cidrs    = ["10.42.1.8/29"]
}
run "approved_subset_and_owner_arns" {
  command = plan
  assert {
    condition     = output.approved_profile.network.allowed_ipv4_cidrs == tolist(["10.42.1.8/29"]) && output.approved_profile.network.subnet_arn == "arn:aws:ec2:ap-northeast-1:999999999999:subnet/subnet-test"
    error_message = "Approved IP range and resource ownership must remain explicit."
  }
}
run "outside_subnet_is_rejected" {
  command = plan
  variables { allowed_ipv4_cidrs = ["10.43.1.0/24"] }
  expect_failures = [aws_launch_template.compute]
}
