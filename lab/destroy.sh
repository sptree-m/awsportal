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
echo "Residual tagged resources:"
LEFT="$(aws resourcegroupstaggingapi get-resources --region "$REGION" --tag-filters Key=Project,Values=awsportal-lab --query 'ResourceTagMappingList[].ResourceARN' --output text || true)"
if [[ -n "$LEFT" ]]; then
  echo "$LEFT"
  echo "WARNING: tagged resources remain."
  exit 2
fi

echo "IAM residual check:"
IAM="$(aws iam list-roles --query "Roles[?contains(RoleName, 'awsportal-lab')].RoleName" --output text || true)"
PROFILES="$(aws iam list-instance-profiles --query "InstanceProfiles[?contains(InstanceProfileName, 'awsportal-lab')].InstanceProfileName" --output text || true)"
if [[ -n "$IAM$PROFILES" ]]; then
  echo "roles=$IAM profiles=$PROFILES"
  exit 2
fi
echo "OK: no tagged or IAM lab resources found."
