mock_provider "aws" {}
run "provided_cur_remains_untouched" {
  command = plan
  variables {
    name                 = "test"
    bucket_name          = "provided-cur"
    portal_role_name     = "provided-portal"
    use_existing_bucket  = true
    create_export        = false
    manage_portal_policy = false
    existing_export_arn  = "arn:aws:bcm-data-exports:us-east-1:123456789012:export/provided"
  }
  assert {
    condition     = length(aws_s3_bucket.cur) == 0 && length(aws_s3_bucket_policy.cur) == 0 && length(aws_bcmdataexports_export.cur) == 0 && length(aws_iam_role_policy.portal) == 0
    error_message = "Existing organization CUR export/bucket/role must be read-only."
  }
}
