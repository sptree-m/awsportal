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
TYPE="${INSTANCE_TYPE:-$(aws ec2 describe-instance-types --region "$REGION" --filters Name=free-tier-eligible,Values=true Name=processor-info.supported-architecture,Values=arm64 --query "InstanceTypes[].InstanceType" --output text | tr "\\t" "\\n" | grep "^t4g\\." | sort | head -1)}"
if [[ -z "$TYPE" || "$TYPE" == "None" ]]; then echo "No Free Tier eligible ARM64 instance type found in $REGION"; exit 1; fi
echo "EC2 instance type: $TYPE"
PASS="Lab-$(openssl rand -hex 12)-A1!"
TOTP="$(openssl rand 20 | base32 | tr -d "=\\n")"
BINARY_URL="${BINARY_URL:-https://github.com/sptree-m/awsportal/releases/download/lab-bootstrap/awsportal-linux-arm64.tar.gz}"
echo "[2/4] deploy $STACK in $REGION; browser=$CIDR"
aws cloudformation deploy --region "$REGION" --stack-name "$STACK" --template-file "$HERE/cloudformation.yaml" --capabilities CAPABILITY_IAM --parameter-overrides AllowedCidr="$CIDR" LabPassword="$PASS" LabTOTPSecret="$TOTP" InstanceType="$TYPE" BinaryURL="$BINARY_URL"
echo "[3/4] outputs"
aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" --query 'Stacks[0].Outputs' --output table
echo "[4/4] login"
echo "username=labadmin"
echo "password=$PASS"
echo "TOTP secret=$TOTP"
echo "TOTP URI=otpauth://totp/awsportal:labadmin?secret=$TOTP&issuer=awsportal"
