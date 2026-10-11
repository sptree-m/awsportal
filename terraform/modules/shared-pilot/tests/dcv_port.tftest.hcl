mock_provider "aws" {
  mock_data "aws_subnet" { defaults = { vpc_id = "vpc-test", availability_zone = "ap-northeast-1a" } }
}
variables {
  name                = "test"
  vpc_id              = "vpc-test"
  subnets_by_az       = { ap-northeast-1a = "subnet-test" }
  users               = { "1" = "user1" }
  golden_ami_id       = "ami-test"
  environment_id      = "1"
  group_id            = "1"
  corporate_cidrs     = ["10.0.0.0/8"]
  metrics_bucket_name = "unused"
  dataset_bucket_arn  = "arn:aws:s3:::provided-dataset"
  portal_role_name    = "provided-portal"
}
run "default_port" {
  command = plan
  assert {
    condition     = one(aws_security_group.compute[0].ingress).from_port == 8443
    error_message = "Default compute DCV ingress must remain TCP/8443."
  }
}
run "custom_port" {
  command = plan
  variables { dcv_port = 10443 }
  assert {
    condition     = one(aws_security_group.compute[0].ingress).from_port == 10443 && one(aws_security_group.compute[0].ingress).to_port == 10443 && one(aws_security_group.compute[0].ingress).protocol == "tcp"
    error_message = "Compute DCV ingress must use the configured TCP port only."
  }
}
run "ssh_port_rejected" {
  command = plan
  variables { dcv_port = 22 }
  expect_failures = [var.dcv_port]
}
run "rdp_port_rejected" {
  command = plan
  variables { dcv_port = 3389 }
  expect_failures = [var.dcv_port]
}
