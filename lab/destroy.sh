#!/usr/bin/env bash
set -euo pipefail
STACK="${STACK:-awsportal-lab}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-ap-northeast-1}}"

echo "Deleting $STACK in $REGION ..."
aws cloudformation delete-stack --region "$REGION" --stack-name "$STACK"
if ! aws cloudformation wait stack-delete-complete --region "$REGION" --stack-name "$STACK"; then
  FAILED="$(aws cloudformation describe-stack-events --region "$REGION" --stack-name "$STACK" --query 'StackEvents[?ResourceStatus==`DELETE_FAILED`].LogicalResourceId' --output text 2>/dev/null | tr '\t' ' ')"
  echo "Initial delete failed: $FAILED"
  if [[ -n "$FAILED" ]]; then
    aws cloudformation delete-stack --region "$REGION" --stack-name "$STACK" --retain-resources $FAILED
    aws cloudformation wait stack-delete-complete --region "$REGION" --stack-name "$STACK"
  fi
fi

echo "CloudFormation stack deleted."
echo "Residual tagged resources (informational; API may lag):"
aws resourcegroupstaggingapi get-resources --region "$REGION" --tag-filters Key=Project,Values=awsportal-lab --query 'ResourceTagMappingList[].ResourceARN' --output text || true

echo "EC2/EBS residual check:"
EC2="$(aws ec2 describe-instances --region "$REGION" --filters Name=tag:Project,Values=awsportal-lab --query 'Reservations[].Instances[?State.Name!=\`terminated\`].InstanceId' --output text)"
EBS="$(aws ec2 describe-volumes --region "$REGION" --filters Name=tag:Project,Values=awsportal-lab --query 'Volumes[].VolumeId' --output text)"
if [[ -n "$EC2$EBS" ]]; then
  echo "instances=$EC2 volumes=$EBS"
  exit 2
fi

echo "VPC/SG residual check:"
VPCS="$(aws ec2 describe-vpcs --region "$REGION" --filters Name=tag:Project,Values=awsportal-lab --query 'Vpcs[].VpcId' --output text)"
SGS="$(aws ec2 describe-security-groups --region "$REGION" --filters Name=tag:Project,Values=awsportal-lab --query 'SecurityGroups[].GroupId' --output text)"
if [[ -n "$VPCS$SGS" ]]; then
  echo "vpcs=$VPCS security-groups=$SGS"
  exit 2
fi

echo "IAM residual check:"
IAM="$(aws iam list-roles --query "Roles[?contains(RoleName, 'awsportal-lab')].RoleName" --output text)"
PROFILES="$(aws iam list-instance-profiles --query "InstanceProfiles[?contains(InstanceProfileName, 'awsportal-lab')].InstanceProfileName" --output text)"
if [[ -n "$IAM$PROFILES" ]]; then
  echo "roles=$IAM profiles=$PROFILES"
  exit 2
fi
echo "OK: no live EC2/EBS/VPC/SG or IAM lab resources found."
