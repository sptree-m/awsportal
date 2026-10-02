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
python3 - <<'PY'
import pathlib,re
root=pathlib.Path("cmd/awsportal/web")
for p in list(root.glob("*.html"))+[root/"app.css"]:
    s=p.read_text()
    if re.search(r'https?://|//(?:cdn|fonts\.)',s,re.I):
        raise SystemExit(f"external runtime URL forbidden: {p}")
    for src in re.findall(r'<script[^>]+src=["\']([^"\']+)["\']',s,re.I):
        if not src.startswith("/static/web/"):
            raise SystemExit(f"non-local script forbidden: {p}: {src}")
    if p.suffix==".css":
        for url in re.findall(r'url\(([^)]+)\)',s,re.I):
            u=url.strip(" \"'")
            if not u.startswith("/static/web/"):
                raise SystemExit(f"non-local CSS URL forbidden: {p}: {u}")
PY
echo 'static security checks: PASS'

echo 'UI network-weight checks'
test "$(wc -c < cmd/awsportal/web/app.css)" -lt 32000
test "$(wc -c < cmd/awsportal/web/dashboard.js)" -lt 4000
test "$(wc -c < cmd/awsportal/web/htmx.min.js)" -lt 60000
! grep -R -E -i 'react|vue|bootstrap|tailwind|jquery|chart\.js|googleapis|gstatic|cdn' cmd/awsportal/web --include='*.html' --include='*.css'
! grep -E -i 'setInterval|EventSource|WebSocket' cmd/awsportal/web/dashboard.js
grep -q 'max-age=86400' cmd/awsportal/main.go
grep -q 'Rounded Mplus 1mn' cmd/awsportal/web/app.css
grep -q 'var htmx=' cmd/awsportal/web/htmx.min.js
test -s cmd/awsportal/web/HTMX_LICENSE.txt
test -s cmd/awsportal/web/fonts/rounded-mplus-1mn-regular.ttf
echo 'UI network-weight checks: PASS'
# The downloadable automation client and repository CLI must stay identical.
cmp scripts/awsportal-mirror cmd/awsportal/web/awsportal-mirror.py
