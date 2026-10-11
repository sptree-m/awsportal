mock_provider "aws" {
  mock_data "aws_region" { defaults = { name = "ap-northeast-1" } }
  mock_data "aws_caller_identity" { defaults = { account_id = "123456789012" } }
}
mock_provider "archive" {}
run "provided_roles_and_shared_vpc_arns" {
  command = plan
  variables {
    name                        = "test"
    resource_class              = "shared"
    credential_prefix           = "/awsportal/shared"
    kms_key_arn                 = "arn:aws:kms:ap-northeast-1:123456789012:key/test"
    portal_role_name            = "provided-portal"
    worker_role_arn             = "arn:aws:iam::123456789012:role/provided-worker"
    workflow_role_arn           = "arn:aws:iam::123456789012:role/provided-workflow"
    manage_portal_policy        = false
    manage_node_policies        = false
    approved_instance_role_arns = ["arn:aws:iam::123456789012:role/provided-compute"]
    approved_pools              = { "1" = { launch_template_id = "lt-test", launch_template_version = "1", ami_id = "ami-test", ami_checksum = "test", network = { vpc_id = "vpc-test", subnet_id = "subnet-provided", subnet_arn = "arn:aws:ec2:ap-northeast-1:999999999999:subnet/subnet-provided", security_group_ids = ["sg-provided"], security_group_arns = ["arn:aws:ec2:ap-northeast-1:999999999999:security-group/sg-provided"], allowed_ipv4_cidrs = ["10.42.1.0/24"], route_table_id = "rtb-test" } } }
  }
  assert {
    condition     = length(aws_iam_role.worker) == 0 && length(aws_iam_role.workflow) == 0 && length(aws_iam_role_policy.worker) == 0 && length(aws_iam_role_policy.workflow) == 0 && length(aws_iam_role_policy.portal) == 0 && length(aws_iam_role_policy.node_bootstrap) == 0
    error_message = "Provided roles must never receive trust or inline policy edits."
  }
  assert {
    condition     = strcontains(output.worker_policy_json, "999999999999:subnet/subnet-provided") && strcontains(output.worker_policy_json, "999999999999:security-group/sg-provided") && !strcontains(output.worker_policy_json, "123456789012:subnet/subnet-provided")
    error_message = "Use organization owner ARNs, not guessed participant account ARNs."
  }
}
