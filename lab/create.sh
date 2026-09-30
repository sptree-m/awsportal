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
echo "[2/4] deploy $STACK in $REGION; browser=$CIDR"
aws cloudformation deploy --region "$REGION" --stack-name "$STACK" --template-file "$HERE/cloudformation.yaml" --capabilities CAPABILITY_IAM --parameter-overrides AllowedCidr="$CIDR"
echo "[3/4] outputs"
aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --query 'Stacks[0].Outputs' --output table
echo "[4/4] waiting for portal bootstrap"
for i in $(seq 1 40); do
  V="$(aws ssm get-parameter --region "$REGION" --name /awsportal-lab/bootstrap --query Parameter.Value --output text 2>/dev/null || true)"
  if [[ "$V" != "pending" && -n "$V" ]]; then echo; echo "LOGIN: $V"; exit 0; fi
  sleep 10
done
echo "Portal EC2 is still bootstrapping. Run: aws ssm get-parameter --name /awsportal-lab/bootstrap --query Parameter.Value --output text"
