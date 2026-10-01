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
echo 'UI dependency checks'
! grep -R -E -i 'https?://|//(cdn|fonts\.)' cmd/awsportal/web --include='*.html' --include='*.css'
! grep -R -E -i '<script[^>]+src=|@import|url\(' cmd/awsportal/web --include='*.html' --include='*.css'
echo 'static security checks: PASS'

echo 'UI network-weight checks'
test "$(wc -c < cmd/awsportal/web/app.css)" -lt 30000
test "$(wc -c < cmd/awsportal/web/dashboard.js)" -lt 12000
! grep -R -E -i 'react|vue|bootstrap|tailwind|jquery|chart\.js|googleapis|gstatic|cdn' cmd/awsportal/web
! grep -R -E -i 'setInterval|EventSource|WebSocket' cmd/awsportal/web --include='*.js'
grep -q 'max-age=86400' cmd/awsportal/main.go
grep -q 'Rounded Mplus 1mn' cmd/awsportal/web/app.css
test -s cmd/awsportal/web/fonts/rounded-mplus-1mn-regular.ttf
echo 'UI network-weight checks: PASS'
