# Build and deployment runbook

## 1. Prerequisites
Terraform >=1.7, Go >=1.23, an ARM64-compatible portal AMI, private subnet connectivity, corporate/VPN CIDRs, internal DNS and a certificate chain trusted by managed PCs.

## 2. Terraform
Create terraform.tfvars with vpc_id, private_subnet_id, corporate_cidrs, portal_ami_id and exact managed_instance_arns. Run terraform fmt -check, terraform validate, terraform plan, peer review the plan, then terraform apply.

## 3. Portal
Build with CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o awsportal ./cmd/awsportal. Install as a dedicated unprivileged systemd service. Terminate HTTPS on the host/reverse proxy using the internal PKI certificate. Never store AWS access keys: use the instance profile.

## 4. Managed desktops
Install and configure Amazon DCV using the current AWS-supported package for the chosen OS. Apply the appropriate permission file. Disable/stop SSH/RDP services where operationally possible and enforce the network boundary in the Security Group. Configure DCV authentication integration only after validating the chosen DCV external-auth flow in a staging instance.

## 5. Acceptance tests
Normal user: only assigned/group EC2 visible; can start/stop/schedule only authorized EC2; DCV connects; SSH/RDP fail; download/clipboard/print/USB/screenshot features unavailable. Admin: TOTP required; all managed EC2 visible; approved download works and is logged. Verify outbound allowlist and audit retention.
