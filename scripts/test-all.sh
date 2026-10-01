#!/usr/bin/env bash
set -euo pipefail
echo '[1/7] Go modules'
go mod tidy
echo '[2/7] Go format'
gofmt -w cmd internal
echo '[3/7] Go test'
go test -race ./...
echo '[4/7] ARM64 build'
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/awsportal-arm64 ./cmd/awsportal
echo '[5/7] Terraform'
terraform -chdir=terraform fmt
terraform -chdir=terraform init -backend=false -input=false >/dev/null
terraform -chdir=terraform validate
echo '[6/7] Security'
bash tests/security.sh
echo '[7/7] Bundled font'
FONT='cmd/awsportal/web/fonts/rounded-mplus-1mn-regular.ttf'
test -s "$FONT"
python3 - "$FONT" <<'PY'
import os, struct, sys
p=sys.argv[1]
with open(p,'rb') as f: head=f.read(4)
if head not in (b'\x00\x01\x00\x00', b'OTTO', b'true', b'typ1'):
    raise SystemExit(f'not a valid sfnt font header: {head!r}')
if os.path.getsize(p) < 10000:
    raise SystemExit('font file is unexpectedly small')
PY
grep -Fq 'font-family:"Rounded Mplus 1mn"' cmd/awsportal/web/app.css
grep -Fq 'rounded-mplus-1mn-regular.ttf' cmd/awsportal/web/app.css
echo '全必須テスト: PASS'
