#!/usr/bin/env bash
set -euo pipefail
! grep -R -E 'from_port[[:space:]]*=[[:space:]]*(22|3389)' terraform
! grep -R -E 'ssm:StartSession|ssm:SendCommand' terraform
! grep -q 'allow.*file-download' dcv/normal-user.perm
! grep -q 'allow.*clipboard-copy' dcv/normal-user.perm
grep -q 'deny.*file-download' dcv/normal-user.perm
grep -q 'http_tokens="required"' terraform/main.tf
echo "static security checks: PASS"
