#!/usr/bin/env bash
set -euo pipefail
ARCH="$(uname -m)"
case "$ARCH" in aarch64|arm64) ;; *) echo "ERROR: ARM64 runnerが必要です: $ARCH"; exit 1;; esac
[ "$(nproc)" -ge 2 ] || { echo 'ERROR: 2 vCPUが必要です'; exit 1; }
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o /tmp/awsportal ./cmd/awsportal
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if command -v systemd-run >/dev/null && systemctl --user show-environment >/dev/null 2>&1; then
 systemd-run --user --unit=awsportal-t4g-test -p MemoryMax=1G -p CPUQuota=200% --collect env AWSPORTAL_DB="$TMP/test.db" AWSPORTAL_ADDR=127.0.0.1:18080 /tmp/awsportal
 for _ in $(seq 1 20); do curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1 && break; sleep .25; done
 curl -fsS http://127.0.0.1:18080/healthz | grep -q '^ok$'
 systemctl --user stop awsportal-t4g-test.service || true
else
 echo 'ERROR: MemoryMax/CPUQuotaを強制できるsystemd user scopeがありません'; exit 1
fi
echo 't4g.micro相当 ARM64/2vCPU/1GiB 試験: PASS'
