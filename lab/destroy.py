#!/usr/bin/env python3
"""Delete only the selected disposable stack; fail closed on AWS API errors."""
import json
import os
import pathlib
import re
import subprocess
import sys

region = os.environ.get("AWS_REGION", os.environ.get("AWS_DEFAULT_REGION", "ap-northeast-1"))
stack = os.environ.get("STACK", "awsportal-lab")
if not re.fullmatch(r"[A-Za-z][A-Za-z0-9-]{0,127}", stack) or not re.fullmatch(r"[a-z0-9-]+", region):
    raise SystemExit("FAIL: invalid stack name or region")
root = pathlib.Path.home() / ".awsportal-lab" / region / stack
root.mkdir(mode=0o700, parents=True, exist_ok=True)
os.umask(0o077)


def aws(*arguments):
    result = subprocess.run(["aws", "--region", region, "--no-cli-pager", *arguments, "--output", "json"],
                            capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError("AWS API failed: " + " ".join(arguments[:2]) + "\n" + result.stderr)
    return json.loads(result.stdout) if result.stdout.strip() else None


def active_stacks():
    return [s for s in aws("cloudformation", "list-stacks")["StackSummaries"]
            if s["StackName"] == stack and s["StackStatus"] != "DELETE_COMPLETE"]


def instances():
    return [i for r in aws("ec2", "describe-instances", "--filters",
                          "Name=tag:aws:cloudformation:stack-name,Values=" + stack)["Reservations"]
            for i in r["Instances"]]


def save(name, value):
    temporary = root / (name + ".tmp")
    temporary.write_text(json.dumps(value))
    temporary.replace(root / name)


def load(name, default):
    path = root / name
    return json.loads(path.read_text()) if path.exists() else default


def main():
    aws("sts", "get-caller-identity")
    before = instances()
    known = load("instances-created.json", {}).get("Reservations", [])
    volumes = set(load("volume-ids.json", []))
    enis = set(load("eni-ids.json", []))
    for instance in before + [i for r in known for i in r["Instances"]]:
        volumes.update(m["Ebs"]["VolumeId"] for m in instance.get("BlockDeviceMappings", []) if "Ebs" in m)
        enis.update(n["NetworkInterfaceId"] for n in instance.get("NetworkInterfaces", []))
    save("volume-ids.json", sorted(volumes))
    save("eni-ids.json", sorted(enis))
    stacks = active_stacks()
    resources = load("stack-resources.json", [])
    if stacks:
        resources = aws("cloudformation", "list-stack-resources", "--stack-name", stacks[0]["StackId"])["StackResourceSummaries"]
        save("stack-resources.json", resources)
        if stacks[0]["StackStatus"] != "DELETE_IN_PROGRESS":
            aws("cloudformation", "delete-stack", "--stack-name", stacks[0]["StackId"])
        print("CloudFormationの削除完了を待ちます...", flush=True)
        aws("cloudformation", "wait", "stack-delete-complete", "--stack-name", stacks[0]["StackId"])
    if active_stacks():
        raise RuntimeError("CloudFormation stack still exists")
    failures = []
    live = [i["InstanceId"] for i in instances() if i["State"]["Name"] != "terminated"]
    if live:
        failures.append("EC2: " + ", ".join(live))
    checks = [
        ("AWS::EC2::Volume", "ec2", "describe-volumes", "Volumes", "VolumeId", volumes),
        ("AWS::EC2::NetworkInterface", "ec2", "describe-network-interfaces", "NetworkInterfaces", "NetworkInterfaceId", enis),
        ("AWS::EC2::VPC", "ec2", "describe-vpcs", "Vpcs", "VpcId", set()),
        ("AWS::EC2::Subnet", "ec2", "describe-subnets", "Subnets", "SubnetId", set()),
        ("AWS::EC2::SecurityGroup", "ec2", "describe-security-groups", "SecurityGroups", "GroupId", set()),
        ("AWS::EC2::InternetGateway", "ec2", "describe-internet-gateways", "InternetGateways", "InternetGatewayId", set()),
        ("AWS::EC2::RouteTable", "ec2", "describe-route-tables", "RouteTables", "RouteTableId", set()),
        ("AWS::IAM::Role", "iam", "list-roles", "Roles", "RoleName", set()),
        ("AWS::IAM::InstanceProfile", "iam", "list-instance-profiles", "InstanceProfiles", "InstanceProfileName", set()),
    ]
    for kind, service, operation, collection, id_field, extra in checks:
        recorded = {r["PhysicalResourceId"] for r in resources if r["ResourceType"] == kind and r.get("PhysicalResourceId")}
        recorded.update(extra)
        current = {r[id_field] for r in aws(service, operation)[collection]}
        remaining = sorted(recorded & current)
        if remaining:
            failures.append(kind + ": " + ", ".join(remaining))
        else:
            print("PASS:", kind, "recorded resources removed")
    if failures:
        raise RuntimeError("残存資源:\n" + "\n".join(failures))
    print("RESULT: PASS - selected stack deleted, EC2 terminated, recorded EBS/network/IAM removed")
    print("terminated EC2の履歴と発生済み料金は残ります。")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, KeyError, ValueError) as error:
        print("RESULT: FAIL -", error, file=sys.stderr)
        sys.exit(2)
