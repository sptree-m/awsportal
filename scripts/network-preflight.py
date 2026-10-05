#!/usr/bin/env python3
"""Read-only AWS profile validation. Never creates or repairs any AWS resource."""
import argparse
import importlib.util
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path, help="approved_profile output saved as JSON")
    parser.add_argument("--region", required=True)
    args = parser.parse_args()
    import boto3
    spec = importlib.util.spec_from_file_location("cloud_control", Path(__file__).resolve().parents[1] / "workflows/cloud_control.py")
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)
    profile = json.loads(args.profile.read_text())
    ec2 = boto3.client("ec2", region_name=args.region)
    template = ec2.describe_launch_template_versions(LaunchTemplateId=profile["launch_template_id"], Versions=[profile["launch_template_version"]])["LaunchTemplateVersions"][0]["LaunchTemplateData"]
    subnet, ranges, auto = worker.network_contract(ec2, profile, template)
    print(json.dumps({"profile_network": profile["network"], "subnet_cidr": str(subnet), "allowed_ipv4_cidrs": [str(r) for r in ranges], "automatic_subnet_allocation": auto, "aws_config_check": "passed", "live_connectivity_acceptance": "pending: test corporate DCV, Portal TLS, EFS, SSM/S3/KMS, DNS, NACLs, TGW return routes and approved Box/Falcon paths"}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
