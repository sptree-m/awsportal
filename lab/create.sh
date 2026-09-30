#!/usr/bin/env bash
set -euo pipefail
STACK="${STACK:-awsportal-lab}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"
HERE="$(cd "$(dirname "$0")" && pwd)"
echo "[1/4] caller"; aws sts get-caller-identity
CIDR="${ALLOWED_CIDR:-}"
if [[ -z "$CIDR" ]]; then
  IP="$(curl -fsS https://checkip.amazonaws.com | tr -d '\n')"
  CIDR="$IP/32"
fi
PASS="Lab-$(openssl rand -hex 12)-A1!"
echo "[2/4] deploy $STACK in $REGION; browser=$CIDR"
aws cloudformation deploy --region "$REGION" --stack-name "$STACK" --template-file "$HERE/cloudformation.yaml" --capabilities CAPABILITY_IAM --parameter-overrides AllowedCidr="$CIDR" LabPassword="$PASS"
echo "[3/4] outputs"
aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --query 'Stacks[0].Outputs' --output table
echo "[4/4] login"
echo "username=labadmin"
echo "password=$PASS"
echo "TOTP URI is stored on the Portal EC2 in /var/lib/awsportal/lab-login.txt"
