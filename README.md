# awsportal

Ultra-lightweight internal AWS EC2/DCV management portal.

## Security model
- End users do **not** receive AWS accounts, IAM users, console access, or access keys.
- Portal uses an EC2 instance profile with least-privilege AWS API permissions.
- Managed desktops are reachable only through Amazon DCV (TCP 8443).
- SSH (22), RDP (3389), SCP and SSM interactive sessions are not user access paths.
- Normal users can see/control only assigned instances or instances assigned to their groups.
- Portal administrators can see all managed instances and must use TOTP MFA.
- DCV download/clipboard/print/USB/screenshot capabilities are denied to normal users.
- Administrative data export is a separate audited privilege.

See `docs/01-architecture.md`, `docs/02-build-runbook.md`, and `docs/03-security-operations.md`.

> Status: initial MVP implementation. Review Terraform variables, IAM policy scope, certificates, DCV version/repository and organization-specific network controls before production deployment.
