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
echo '[7/7] Bundled fonts'
python3 - <<'PY'
import os, struct
fonts=[
 ("cmd/awsportal/web/fonts/rounded-mplus-1mn-regular.ttf",400),
 ("cmd/awsportal/web/fonts/rounded-mplus-1mn-bold.ttf",700),
]
for p, expected_weight in fonts:
    if not os.path.isfile(p) or os.path.getsize(p)<10000:
        raise SystemExit(f"missing/small font: {p}")
    b=open(p,"rb").read()
    if b[:4] not in (b"\x00\x01\x00\x00",b"OTTO",b"true",b"typ1"):
        raise SystemExit(f"invalid sfnt header: {p}")
    num=struct.unpack(">H",b[4:6])[0]
    tables={}
    for i in range(num):
        off=12+i*16
        tag=b[off:off+4]
        _,offset,length=struct.unpack(">III",b[off+4:off+16])
        tables[tag]=(offset,length)
    if b"OS/2" not in tables:
        raise SystemExit(f"OS/2 table missing: {p}")
    o,_=tables[b"OS/2"]
    weight=struct.unpack(">H",b[o+4:o+6])[0]
    if weight!=expected_weight:
        raise SystemExit(f"wrong weight {weight}, expected {expected_weight}: {p}")
PY
grep -Fq 'rounded-mplus-1mn-regular.ttf' cmd/awsportal/web/app.css
grep -Fq 'rounded-mplus-1mn-bold.ttf' cmd/awsportal/web/app.css
grep -Fq 'font-weight:400' cmd/awsportal/web/app.css
grep -Fq 'font-weight:700' cmd/awsportal/web/app.css
echo '全必須テスト: PASS'
