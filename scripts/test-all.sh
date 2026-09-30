#!/usr/bin/env bash
set -euo pipefail
echo '[1/6] Go modules'
go mod tidy
echo '[2/6] Go format'
gofmt -w cmd internal
echo '[3/6] Go test'
go test -race ./...
echo '[4/6] ARM64 build'
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/awsportal-arm64 ./cmd/awsportal
echo '[5/6] Terraform'
terraform -chdir=terraform fmt
terraform -chdir=terraform init -backend=false -input=false >/dev/null
terraform -chdir=terraform validate
echo '[6/6] Security'
bash tests/security.sh
echo '全必須テスト: PASS'
