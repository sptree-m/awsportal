#!/usr/bin/env bash
set -euo pipefail
ARCH="$(uname -m)"
case "$ARCH" in aarch64|arm64) ;; *) echo "ERROR: ARM64 runnerが必要です: $ARCH"; exit 1;; esac
go mod tidy
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o /tmp/awsportal ./cmd/awsportal
docker rm -f awsportal-t4g-test >/dev/null 2>&1 || true
trap 'docker rm -f awsportal-t4g-test >/dev/null 2>&1 || true' EXIT
docker run -d --name awsportal-t4g-test --memory=1g --cpus=2 -p 18080:8080 -e AWSPORTAL_DB=/tmp/test.db -e AWSPORTAL_COOKIE_SECURE=0 -v /tmp/awsportal:/usr/local/bin/awsportal:ro ubuntu:24.04 /usr/local/bin/awsportal >/dev/null
for _ in $(seq 1 30); do curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1 && break; sleep .5; done
curl -fsS http://127.0.0.1:18080/healthz | grep -q '^ok$'
MEM="$(docker inspect -f '{{.HostConfig.Memory}}' awsportal-t4g-test)"
CPUS="$(docker inspect -f '{{.HostConfig.NanoCpus}}' awsportal-t4g-test)"
[ "$MEM" = "1073741824" ]
[ "$CPUS" = "2000000000" ]
echo 't4g.micro相当 ARM64/2vCPU/1GiB 試験: PASS'
