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

run "provided_bucket_export_region" {
  command = plan
  variables {
    name                   = "test"
    bucket_name            = "provided-cur"
    portal_role_name       = "provided-portal"
    use_existing_bucket    = true
    existing_bucket_region = "ap-northeast-1"
    create_export          = true
    manage_portal_policy   = false
  }
  assert {
    condition     = aws_bcmdataexports_export.cur[0].export[0].destination_configurations[0].s3_destination[0].s3_region == "ap-northeast-1" && length(aws_s3_bucket_policy.cur) == 0
    error_message = "Export must target the provided bucket region without changing its policy."
  }
}
