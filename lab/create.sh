#!/usr/bin/env bash
set -Eeuo pipefail
STACK="${STACK:-awsportal-lab}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"
HERE="$(cd "$(dirname "$0")" && pwd)"
BINARY_URL="${BINARY_URL:-https://github.com/sptree-m/awsportal/releases/download/v1.2.1/awsportal-v1.2.1-linux-arm64.tar.gz}"
fail(){ rc=$?; echo; echo "RESULT: FAIL - create/verification failed (exit=$rc)"; exit "$rc"; }
trap fail ERR

echo "============================================================"
echo " AWSPORTAL LAB CREATE & VERIFICATION"
echo "============================================================"
echo "[1/6] AWS credentials"
aws sts get-caller-identity >/dev/null
echo "[OK] AWS credentials"

CIDR="${ALLOWED_CIDR:-0.0.0.0/0}"
echo "[2/6] Browser CIDR: $CIDR"
TYPE="${INSTANCE_TYPE:-t4g.micro}"
ARCH="$(aws ec2 describe-instance-types --region "$REGION" --instance-types "$TYPE" --query 'InstanceTypes[0].ProcessorInfo.SupportedArchitectures' --output text)"
grep -qw arm64 <<<"$ARCH"
echo "[3/6] Instance type: $TYPE (ARM64 verified)"

PASS="Lab-$(openssl rand -hex 12)-A1!"
TOTP="$(openssl rand 20 | base32 | tr -d '=\n')"
echo "[4/6] Deploying $STACK"
aws cloudformation deploy --region "$REGION" --stack-name "$STACK" --template-file "$HERE/cloudformation.yaml" --capabilities CAPABILITY_IAM --parameter-overrides AllowedCidr="$CIDR" LabPassword="$PASS" LabTOTPSecret="$TOTP" InstanceType="$TYPE" BinaryURL="$BINARY_URL"

PORTAL_ID="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --query 'Stacks[0].Outputs[?OutputKey==`PortalInstanceId`].OutputValue' --output text)"
TEST_ID="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --query 'Stacks[0].Outputs[?OutputKey==`TestInstanceId`].OutputValue' --output text)"
URL="$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --query 'Stacks[0].Outputs[?OutputKey==`PortalURL`].OutputValue' --output text)"
echo "[5/6] EC2 verification"
aws ec2 wait instance-running --region "$REGION" --instance-ids "$PORTAL_ID" "$TEST_ID"
LIVE_ARCH="$(aws ec2 describe-instances --region "$REGION" --instance-ids "$PORTAL_ID" "$TEST_ID" --query 'Reservations[].Instances[].Architecture' --output text)"
[[ "$(tr '\t' '\n' <<<"$LIVE_ARCH" | grep -cx arm64)" -eq 2 ]]
echo "[OK] Portal EC2: $PORTAL_ID / arm64 / running"
echo "[OK] Test EC2:   $TEST_ID / arm64 / running"

echo "[6/6] Application health"
HEALTH=""
for attempt in $(seq 1 60); do
  if [[ "$(curl -fsS --max-time 3 "$URL/healthz" 2>/dev/null || true)" == "ok" ]]; then HEALTH=ok; break; fi
  echo "  waiting for /healthz... $((attempt * 5))s"
  sleep 5
done
if [[ "$HEALTH" != "ok" ]]; then
  echo "[ERROR] /healthz did not become ready"
  echo "[DIAG] Collecting cloud-init/systemd diagnostics via SSM"
  CMD_ID="$(aws ssm send-command --region "$REGION" --instance-ids "$PORTAL_ID" --document-name AWS-RunShellScript --parameters '{"commands":["sudo cloud-init status --long || true","sudo systemctl status awsportal --no-pager -l || true","sudo journalctl -u awsportal -n 80 --no-pager || true","sudo tail -n 100 /var/log/cloud-init-output.log || true","ls -l /usr/local/bin/awsportal* /var/lib/awsportal 2>&1 || true"]}' --query 'Command.CommandId' --output text || true)"
  if [[ -n "$CMD_ID" && "$CMD_ID" != "None" ]]; then
    aws ssm wait command-executed --region "$REGION" --command-id "$CMD_ID" --instance-id "$PORTAL_ID" || true
    aws ssm get-command-invocation --region "$REGION" --command-id "$CMD_ID" --instance-id "$PORTAL_ID" --query '{Status:Status,Output:StandardOutputContent,Error:StandardErrorContent}' --output json || true
  fi
  false
fi
echo "[OK] /healthz: ok"

echo "------------------------------------------------------------"
echo "RESULT: PASS - ARM lab deployment and application health verified"
echo "Portal URL: $URL"
echo "username=labadmin"
echo "password=$PASS"
echo "TOTP secret=$TOTP"
echo "TOTP URI=otpauth://totp/awsportal:labadmin?secret=$TOTP&issuer=awsportal"
echo "============================================================"
trap - ERR
