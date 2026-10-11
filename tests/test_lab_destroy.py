import contextlib
import importlib.util
import io
import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch


class LabDestroyTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        with patch.dict(os.environ, {"HOME": self.temp.name, "STACK": "awsportal-test", "AWS_REGION": "ap-northeast-1"}):
            spec = importlib.util.spec_from_file_location("lab_destroy", pathlib.Path(__file__).parents[1] / "lab/destroy.py")
            self.module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(self.module)
        self.deleted = False
        self.calls = []
        self.retain_volume = False
        self.deny_volumes = False
        self.module.aws = self.aws

    def aws(self, *args):
        self.calls.append(args)
        service, operation = args[:2]
        if service == "sts":
            return {"Account": "test"}
        if service == "cloudformation":
            if operation == "list-stacks":
                return {"StackSummaries": [] if self.deleted else [{"StackName": "awsportal-test", "StackId": "selected-id", "StackStatus": "CREATE_COMPLETE"}]}
            if operation == "list-stack-resources":
                return {"StackResourceSummaries": [{"ResourceType": "AWS::EC2::VPC", "PhysicalResourceId": "vpc-test"}]}
            if operation == "delete-stack":
                self.assertEqual(args[-1], "selected-id")
                self.deleted = True
                return None
            return None  # waiter
        if operation == "describe-instances":
            return {"Reservations": [{"Instances": [{"InstanceId": "i-test", "State": {"Name": "terminated" if self.deleted else "running"},
                "BlockDeviceMappings": [] if self.deleted else [{"Ebs": {"VolumeId": "vol-test"}}]}]}]}
        if operation == "describe-volumes":
            if self.deny_volumes:
                raise RuntimeError("AWS API failed: AccessDenied")
            return {"Volumes": [{"VolumeId": "vol-other"}] + ([{"VolumeId": "vol-test"}] if self.retain_volume else [])}
        mapping = {
            "describe-network-interfaces": "NetworkInterfaces", "describe-vpcs": "Vpcs", "describe-subnets": "Subnets",
            "describe-security-groups": "SecurityGroups", "describe-internet-gateways": "InternetGateways",
            "describe-route-tables": "RouteTables", "list-roles": "Roles", "list-instance-profiles": "InstanceProfiles",
        }
        return {mapping[operation]: []}

    def test_checks_root_volume_id_without_relying_on_volume_tags(self):
        self.retain_volume = True
        with contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(RuntimeError, "vol-test"):
            self.module.main()

    def test_api_error_is_not_success(self):
        self.deny_volumes = True
        with contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(RuntimeError, "AccessDenied"):
            self.module.main()

    def test_only_selected_stack_deleted_and_unrelated_volume_ignored(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.module.main()
        self.assertIn("RESULT: PASS", output.getvalue())
        self.assertEqual(sum(args[1] == "delete-stack" for args in self.calls), 1)
        # A repeated cleanup verifies persisted volume IDs even if the stack is gone.
        self.retain_volume = True
        with contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(RuntimeError, "vol-test"):
            self.module.main()


if __name__ == "__main__":
    unittest.main()
