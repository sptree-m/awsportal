#!/usr/bin/env bash
set -euo pipefail
echo '[1/5] Go format'
test -z "$(gofmt -l cmd internal)"
echo '[2/5] Go test'
go test ./...
echo '[3/5] ARM64 build'
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/awsportal-arm64 ./cmd/awsportal
echo '[4/5] Terraform'
terraform -chdir=terraform fmt -check
terraform -chdir=terraform init -backend=false -input=false >/dev/null
terraform -chdir=terraform validate
echo '[5/5] Security'
bash tests/security.sh
echo '全必須テスト: PASS'
