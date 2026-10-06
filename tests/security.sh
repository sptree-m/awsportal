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
grep -q -E 'from_port[[:space:]]*=[[:space:]]*var\.dcv_port' terraform/main.tf
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

# Keep the product principles in current documentation, including after doc cleanup.
echo 'Product principle documentation checks'
python3 - <<'PYDOC'
from pathlib import Path
readme = Path("README.md").read_text()
heading = "## 最優先のコンセプト"
if heading not in readme:
    raise SystemExit("README must retain the product principles section")
section = readme.split(heading, 1)[1].split("\n## ", 1)[0]
for principle in ("超軽量", "高速", "高いメンテナンス性", "プロ仕様UI"):
    if f"| {principle} |" not in section:
        raise SystemExit(f"product principle missing: {principle}")
if "削除・弱体化は禁止" not in section:
    raise SystemExit("README must retain the principle preservation rule")
for filename, target in (
    ("docs/README.md", "../README.md#最優先のコンセプト"),
    ("docs/architecture/overview.md", "../../README.md#最優先のコンセプト"),
    ("docs/development/ui.md", "../../README.md#最優先のコンセプト"),
):
    if f"({target})" not in Path(filename).read_text():
        raise SystemExit(f"product principle reference missing: {filename}")
PYDOC
echo 'Product principle documentation checks: PASS'
