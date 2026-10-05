mock_provider "aws" {
  mock_data "aws_subnet" { defaults = { vpc_id = "vpc-test", availability_zone = "ap-northeast-1a" } }
  mock_data "aws_security_group" { defaults = { vpc_id = "vpc-test" } }
  mock_data "aws_efs_file_system" { defaults = { id = "fs-user", encrypted = true } }
  mock_data "aws_efs_access_point" { defaults = { file_system_id = "fs-user", posix_user = [], root_directory = [{ path = "/home", creation_info = [{ owner_uid = 200001, owner_gid = 200001, permissions = "0700" }] }] } }
}
run "provided_storage_roles_and_sgs_are_untouched" {
  command = plan
  variables {
    name                                   = "test"
    vpc_id                                 = "vpc-test"
    subnets_by_az                          = { ap-northeast-1a = "subnet-test" }
    users                                  = { "1" = "user1" }
    golden_ami_id                          = "ami-test"
    environment_id                         = "1"
    group_id                               = "1"
    corporate_cidrs                        = ["10.0.0.0/8"]
    metrics_bucket_name                    = "unused"
    existing_metrics_bucket_name           = "provided-usage"
    dataset_bucket_arn                     = "arn:aws:s3:::provided-dataset"
    portal_role_name                       = "provided-portal"
    existing_compute_security_group_ids    = ["sg-compute"]
    existing_storage_security_group_id     = "sg-storage"
    existing_compute_role_arn              = "arn:aws:iam::123456789012:role/provided"
    existing_compute_instance_profile_name = "provided"
    existing_compute_instance_profile_arn  = "arn:aws:iam::123456789012:instance-profile/provided"
    existing_user_storage                  = { "1" = { efs_id = "fs-user", efs_arn = "arn:aws:elasticfilesystem:ap-northeast-1:123456789012:file-system/fs-user", access_point_id = "fsap-user", access_point_arn = "arn:aws:elasticfilesystem:ap-northeast-1:123456789012:access-point/fsap-user" } }
    existing_group_storage                 = { efs_id = "fs-group", efs_arn = "arn:aws:elasticfilesystem:ap-northeast-1:123456789012:file-system/fs-group", access_point_id = "fsap-group", access_point_arn = "arn:aws:elasticfilesystem:ap-northeast-1:123456789012:access-point/fsap-group" }
    manage_peer_security_group_rules       = false
    manage_portal_policies                 = false
  }
  assert {
    condition     = length(aws_iam_role.compute) == 0 && length(aws_iam_role_policy.compute) == 0 && length(aws_iam_role_policy.group) == 0 && length(aws_security_group.compute) == 0 && length(aws_security_group.storage) == 0 && length(aws_security_group_rule.nfs) == 0 && length(aws_security_group_rule.portal_from_compute) == 0 && length(aws_security_group_rule.endpoints_from_compute) == 0 && length(aws_efs_file_system.user) == 0 && length(aws_efs_file_system.group) == 0 && length(aws_efs_mount_target.home) == 0 && length(aws_efs_mount_target.group) == 0 && length(aws_s3_bucket.usage) == 0
    error_message = "Provided resources must never receive create/update/delete operations."
  }
}
