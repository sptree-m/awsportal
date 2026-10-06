mock_provider "aws" {
  mock_data "aws_subnet" { defaults = { vpc_id = "vpc-test" } }
}
variables {
  vpc_id                      = "vpc-test"
  private_subnet_id           = "subnet-test"
  corporate_cidrs             = ["10.0.0.0/8"]
  portal_ami_id               = "ami-test"
  managed_instance_arns       = []
  managed_egress_instance_ids = ["i-test"]
}
run "default_port" {
  command = plan
  assert {
    condition     = one(aws_security_group.dcv_only[0].ingress).from_port == 8443 && one(aws_security_group.managed_egress["i-test"].ingress).to_port == 8443
    error_message = "Default DCV and managed egress ingress must remain TCP/8443."
  }
}
run "custom_port" {
  command = plan
  variables { dcv_port = 443 }
  assert {
    condition     = alltrue([for rule in concat(tolist(aws_security_group.dcv_only[0].ingress), tolist(aws_security_group.managed_egress["i-test"].ingress)) : rule.from_port == 443 && rule.to_port == 443 && rule.protocol == "tcp" && toset(rule.cidr_blocks) == toset(["10.0.0.0/8"])])
    error_message = "Both DCV ingress paths must use only the configured TCP port and corporate CIDRs."
  }
}
run "reserved_port" {
  command = plan
  variables { dcv_port = 8444 }
  expect_failures = [var.dcv_port]
}
run "out_of_range_port" {
  command = plan
  variables { dcv_port = 65536 }
  expect_failures = [var.dcv_port]
}
run "fractional_port" {
  command = plan
  variables { dcv_port = 443.5 }
  expect_failures = [var.dcv_port]
}
