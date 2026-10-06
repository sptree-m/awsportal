# Fixed Shared pilot module

Opt-in preparation module for the stage-one connection foundation. See
[current environment operations and acceptance requirements](../../../docs/operations/environments.md).
This module alone does not satisfy the stage-one release acceptance conditions.

Use it from an IT-managed Terraform root. Supply existing approved networking,
Golden AMI and IAM resources. Do not apply it to a VPC inferred from defaults.

```hcl
module "shared_pilot" {
  source                      = "./modules/shared-pilot"
  name                        = "shared-pilot"
  vpc_id                      = var.vpc_id
  subnets_by_az               = var.approved_private_subnets
  users                       = var.pilot_users # Stable Portal ID => display name
  golden_ami_id               = var.approved_shared_cpu_ami
  environment_id              = var.shared_environment_id
  group_id                    = var.pilot_group_id
  corporate_cidrs             = var.corporate_cidrs
  portal_security_group_id    = aws_security_group.portal.id
  endpoint_security_group_id  = var.approved_endpoint_sg
  s3_prefix_list_id            = var.approved_s3_prefix_list
  metrics_bucket_name         = var.shared_metrics_bucket
  dataset_bucket_arn          = var.approved_dataset_bucket_arn
  portal_role_name            = aws_iam_role.portal.name
}
```

EFS and Access Points have `prevent_destroy`; username changes do not change
filesystem keys. No EFS lifecycle/archive policy is enabled. AWS Backup is enabled,
but backup plan/retention and restore validation must be approved separately.
The access point preserves actual client UID/GID, rather than overriding every
request to the same owner identity. ClientRootAccess is denied. General users
must have no sudo, mount capability or access to instance-role credentials.

No public IP, generic Internet egress, SSH or RDP access is created. Verify private
DNS, route tables, endpoint policy and corporate routing separately. Dataset and
quota setup, Group EFS, CUR and dynamic provisioning are follow-up changes.
