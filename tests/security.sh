#!/usr/bin/env bash
set -euo pipefail
echo 'Security policy checks'
! grep -R -E 'from_port[[:space:]]*=[[:space:]]*(22|3389)' terraform
! grep -R -E 'ssm:StartSession|ssm:SendCommand' terraform
! grep -q -E 'allow.*file-download' dcv/normal-user.perm
! grep -q -E 'allow.*clipboard-copy' dcv/normal-user.perm
grep -q -E 'deny.*file-download' dcv/normal-user.perm
grep -q -E 'deny.*clipboard-copy' dcv/normal-user.perm
grep -q -E 'http_tokens[[:space:]]*=[[:space:]]*"required"' terraform/main.tf
grep -q -E 'from_port[[:space:]]*=[[:space:]]*8443' terraform/main.tf
grep -q -E 'encrypted[[:space:]]*=[[:space:]]*true' terraform/main.tf
echo 'static security checks: PASS'
