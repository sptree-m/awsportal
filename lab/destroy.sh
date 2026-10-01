#!/usr/bin/env bash
set -uo pipefail
STACK="${STACK:-awsportal-lab}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"
PROJECT="${PROJECT:-awsportal-lab}"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
FAIL=0
ok(){ printf '[OK]   %-28s %s\n' "$1" "${2:-}"; }
bad(){ printf '[FAIL] %-28s %s\n' "$1" "${2:-}"; FAIL=1; }

echo "============================================================"
echo " AWSPORTAL LAB DESTROY VERIFICATION"
echo "============================================================"
aws sts get-caller-identity >/dev/null 2>&1 || { bad "AWS credentials" "unavailable"; exit 2; }
ok "AWS credentials"

# Snapshot CloudFormation physical IDs before deletion so verification is not tag-only.
if aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" >/dev/null 2>&1; then
  aws cloudformation list-stack-resources --region "$REGION" --stack-name "$STACK" \
    --query 'StackResourceSummaries[].[ResourceType,PhysicalResourceId]' --output text >"$TMP/resources" || true
  aws cloudformation delete-stack --region "$REGION" --stack-name "$STACK"
  if aws cloudformation wait stack-delete-complete --region "$REGION" --stack-name "$STACK"; then ok "CloudFormation delete" "complete"; else bad "CloudFormation delete" "wait failed"; fi
else
  : >"$TMP/resources"; ok "CloudFormation stack" "already absent"
fi

# Stack must be gone.
if aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" >/dev/null 2>&1; then bad "CloudFormation stack" "still exists"; else ok "CloudFormation stack" "not found"; fi

# Tagging API can temporarily retain terminated EC2 ARNs. Report them, but
# determine failure from the authoritative service APIs below.
TAGGED="$(aws resourcegroupstaggingapi get-resources --region "$REGION" --tag-filters Key=Project,Values="$PROJECT" --query 'ResourceTagMappingList[].ResourceARN' --output text 2>/dev/null || true)"
TAGGED_COUNT="$(wc -w <<<"$TAGGED" | tr -d ' ')"
[[ -z "$TAGGED" ]] && TAGGED_COUNT=0
ok "Tag API references" "$TAGGED_COUNT (informational)"

TERMINATED="$(aws ec2 describe-instances --region "$REGION" --filters Name=tag:Project,Values="$PROJECT" Name=instance-state-name,Values=terminated --query 'Reservations[].Instances[].InstanceId' --output text 2>/dev/null || true)"
TERMINATED_COUNT="$(wc -w <<<"$TERMINATED" | tr -d ' ')"
[[ -z "$TERMINATED" ]] && TERMINATED_COUNT=0
ok "EC2 terminated (history)" "$TERMINATED_COUNT"
[[ "$TERMINATED_COUNT" -gt 0 ]] && printf '       %s\n' "$TERMINATED"

EC2="$(aws ec2 describe-instances --region "$REGION" --filters Name=tag:Project,Values="$PROJECT" --query 'Reservations[].Instances[?State.Name!=`terminated`].InstanceId' --output text 2>/dev/null || true)"
EBS="$(aws ec2 describe-volumes --region "$REGION" --filters Name=tag:Project,Values="$PROJECT" --query 'Volumes[].VolumeId' --output text 2>/dev/null || true)"
ENI="$(aws ec2 describe-network-interfaces --region "$REGION" --filters Name=tag:Project,Values="$PROJECT" --query 'NetworkInterfaces[].NetworkInterfaceId' --output text 2>/dev/null || true)"
VPC="$(aws ec2 describe-vpcs --region "$REGION" --filters Name=tag:Project,Values="$PROJECT" --query 'Vpcs[].VpcId' --output text 2>/dev/null || true)"
SG="$(aws ec2 describe-security-groups --region "$REGION" --filters Name=tag:Project,Values="$PROJECT" --query 'SecurityGroups[].GroupId' --output text 2>/dev/null || true)"
for pair in "EC2:$EC2" "EBS:$EBS" "ENI:$ENI" "VPC:$VPC" "Security Groups:$SG"; do n="${pair%%:*}"; v="${pair#*:}"; [[ -z "$v" ]] && ok "$n" "0" || bad "$n" "$v"; done

IAM="$(aws iam list-roles --query "Roles[?contains(RoleName, '$STACK')].RoleName" --output text 2>/dev/null || true)"
PROF="$(aws iam list-instance-profiles --query "InstanceProfiles[?contains(InstanceProfileName, '$STACK')].InstanceProfileName" --output text 2>/dev/null || true)"
[[ -z "$IAM" ]] && ok "IAM Roles" "0" || bad "IAM Roles" "$IAM"
[[ -z "$PROF" ]] && ok "Instance Profiles" "0" || bad "Instance Profiles" "$PROF"

# Verify recorded physical IDs when possible.
while IFS=$'\t' read -r type id; do
  [[ -z "$id" || "$id" == "None" ]] && continue
  case "$type" in
    AWS::EC2::Volume) aws ec2 describe-volumes --region "$REGION" --volume-ids "$id" >/dev/null 2>&1 && bad "Recorded Volume" "$id still exists" ;;
    AWS::EC2::NetworkInterface) aws ec2 describe-network-interfaces --region "$REGION" --network-interface-ids "$id" >/dev/null 2>&1 && bad "Recorded ENI" "$id still exists" ;;
    AWS::EC2::VPC) aws ec2 describe-vpcs --region "$REGION" --vpc-ids "$id" >/dev/null 2>&1 && bad "Recorded VPC" "$id still exists" ;;
    AWS::EC2::Subnet) aws ec2 describe-subnets --region "$REGION" --subnet-ids "$id" >/dev/null 2>&1 && bad "Recorded Subnet" "$id still exists" ;;
    AWS::EC2::SecurityGroup) aws ec2 describe-security-groups --region "$REGION" --group-ids "$id" >/dev/null 2>&1 && bad "Recorded SG" "$id still exists" ;;
    AWS::EC2::InternetGateway) aws ec2 describe-internet-gateways --region "$REGION" --internet-gateway-ids "$id" >/dev/null 2>&1 && bad "Recorded IGW" "$id still exists" ;;
    AWS::EC2::RouteTable) aws ec2 describe-route-tables --region "$REGION" --route-table-ids "$id" >/dev/null 2>&1 && bad "Recorded RouteTable" "$id still exists" ;;
  esac
done <"$TMP/resources"

echo "------------------------------------------------------------"
if (( FAIL )); then echo "RESULT: FAIL - residual resources or deletion errors detected"; exit 2; fi
echo "RESULT: PASS - no live awsportal-lab resources detected (terminated EC2 history is informational)"
echo "============================================================"
