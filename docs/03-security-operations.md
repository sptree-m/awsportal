# Security and operations

- Portal admin != AWS infrastructure admin. Keep the roles separate.
- Require TOTP for portal_admin and recovery procedures with dual control.
- Hash passwords with bcrypt; encrypt TOTP seeds at rest with a production key-management design before launch.
- Use IMDSv2 and an EC2 instance profile. No static AWS keys.
- Tag managed instances and scope IAM to the intended resources where AWS APIs support resource-level permissions.
- Treat SQLite backups, audit logs and TOTP secrets as sensitive.
- Review Security Groups for accidental 22/3389 exposure.
- Do not enable Session Manager interactive access as an end-user bypass.
- Enforce outbound allowlisting with the organization's approved firewall/proxy architecture. The sample Terraform intentionally does not pretend its simple 443 egress rule is production DLP.
- CloudTrail, VPC Flow Logs and central log retention should be enabled by the platform account baseline or added before production.
- Emergency access must be documented, time-limited and audited.
