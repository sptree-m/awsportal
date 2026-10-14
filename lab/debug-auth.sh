#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"
STACK="${STACK:-awsportal-lab}"

if [[ "$MODE" != "enable" && "$MODE" != "disable" ]]; then
  echo "Usage: $0 enable|disable (labdebug uses password+TOTP; no bypass)"
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

if [[ "$MODE" == "enable" ]]; then
  SQL="UPDATE users SET enabled=1, role='portal_admin' WHERE username='labdebug';"
  echo "password＋TOTPのラボ管理者を有効化します"
else
  SQL="UPDATE users SET enabled=0 WHERE username='labdebug';"
  echo "ラボ管理者を無効化します"
fi

CMD_ID="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$PORTAL_ID" \
  --document-name AWS-RunShellScript \
  --parameters "$(python3 -c 'import json,sys; print(json.dumps({"commands":[sys.argv[1]]}))' "sudo sqlite3 /var/lib/awsportal/awsportal.db \"$SQL\"")" \
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

echo "Lab debug account: $MODE (password+TOTP required; credentials from lab creation)"
