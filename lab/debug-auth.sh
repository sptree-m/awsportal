#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"
STACK="${STACK:-awsportal-lab}"

if [[ "$MODE" != "on" && "$MODE" != "off" ]]; then
  echo "Usage: $0 on|off"
  echo "  off = MFA不要のlabdebugを有効化"
  echo "  on  = labdebugを無効化"
  exit 2
fi

PORTAL_ID="$(aws cloudformation describe-stacks \
  --region "$REGION" \
  --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='PortalInstanceId'].OutputValue | [0]" \
  --output text)"

if [[ -z "$PORTAL_ID" || "$PORTAL_ID" == "None" ]]; then
  echo "Portal EC2が見つかりません"
  exit 1
fi

if [[ "$MODE" == "off" ]]; then
  SQL="UPDATE users SET enabled=1 WHERE username='labdebug';"
  echo "MFAなしデバッグログインを有効化します"
else
  SQL="UPDATE users SET enabled=0 WHERE username='labdebug';"
  echo "MFAなしデバッグログインを無効化します"
fi

CMD_ID="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$PORTAL_ID" \
  --document-name AWS-RunShellScript \
  --parameters commands="sudo sqlite3 /var/lib/awsportal/awsportal.db \"$SQL\"" \
  --query 'Command.CommandId' \
  --output text)"

aws ssm wait command-executed \
  --region "$REGION" \
  --command-id "$CMD_ID" \
  --instance-id "$PORTAL_ID"

aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$CMD_ID" \
  --instance-id "$PORTAL_ID" \
  --query '{Status:Status,Output:StandardOutputContent,Error:StandardErrorContent}' \
  --output table

echo
if [[ "$MODE" == "off" ]]; then
  echo "DEBUG AUTH: OFF (labdebug有効 / MFA不要)"
  echo
  echo "=== Debug login ==="

  aws ssm send-command     --region "$REGION"     --instance-ids "$PORTAL_ID"     --document-name AWS-RunShellScript     --parameters 'commands=["sudo grep ^debug_ /var/lib/awsportal/lab-login.txt"]'     --query 'Command.CommandId'     --output text

  echo "username: labdebug"
  echo "password: Portal EC2の /var/lib/awsportal/lab-login.txt に保存"
else
  echo "DEBUG AUTH: ON (labdebug無効 / Portal Admin MFA必須)"
fi
