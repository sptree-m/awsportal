import csv
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location('aws_audit', pathlib.Path(__file__).parents[1] / 'tools/aws-audit/audit.py')
audit = importlib.util.module_from_spec(spec)
spec.loader.exec_module(audit)


class AuditTest(unittest.TestCase):
    def test_classification_is_conservative(self):
        self.assertEqual(audit.action('s3api', 'list-buckets'), 's3:ListAllMyBuckets')
        self.assertEqual(audit.action('efs', 'describe-file-systems'), 'elasticfilesystem:DescribeFileSystems')
        cases = [
            (255, '(DryRunOperation) when calling X', True, 'ALLOW'),
            (255, '(UnauthorizedOperation) when calling X', True, 'DENY'),
            (255, '(AccessDeniedException) when calling X', False, 'DENY'),
            (255, '(InvalidInstanceID.NotFound) when calling X', True, 'UNKNOWN'),
            (255, 'endpoint unavailable', False, 'UNKNOWN'),
            (0, '', True, 'UNKNOWN'),
        ]
        for rc, err, dry, expected in cases:
            self.assertEqual(audit.classify(rc, err, dry)[0], expected)

    def test_config_rejects_disabling_dryrun_and_unknown_operations(self):
        with tempfile.TemporaryDirectory() as tmp:
            p = pathlib.Path(tmp) / 'requests.json'
            req = dict(operation='delete-volume', region='us-east-1', scenario='test', resource='vol-test', parameters={'DryRun': False})
            for change in ({}, {'operation': 'delete-bucket', 'parameters': {}}, {'region': 'us-west-2', 'parameters': {}}):
                p.write_text(json.dumps([dict(req, **change)]))
                with self.assertRaises(ValueError):
                    audit.validate_requests(p, ['us-east-1'])

    def test_full_collection_preserves_evidence_and_never_mutates(self):
        calls = []
        identity = {'Account': '123456789012', 'Arn': 'arn:aws:sts::123456789012:assumed-role/MyRole/session', 'UserId': 'roleid:session'}

        def fake(cmd, **kwargs):
            service, op = cmd[1:3]
            params = json.loads(cmd[cmd.index('--cli-input-json') + 1]) if '--cli-input-json' in cmd else {}
            calls.append((service, op, params))
            if not op.startswith(('get-', 'list-', 'describe-')) and op != 'lookup-events':
                self.assertEqual(service, 'ec2')
                self.assertIs(params['DryRun'], True)
                code = 'UnauthorizedOperation' if op == 'delete-volume' else 'DryRunOperation'
                return subprocess.CompletedProcess(cmd, 255, '', f'An error occurred ({code}) when calling X')
            if service == 'sts':
                data = identity
            elif op == 'get-role':
                data = {'Role': {'PermissionsBoundary': {'PermissionsBoundaryArn': 'arn:aws:iam::123456789012:policy/boundary'}}}
            elif op == 'get-policy':
                data = {'Policy': {'DefaultVersionId': 'v3'}}
            elif op == 'list-parents':
                data = {'Parents': [{'Id': 'ou-test'}]} if params['ChildId'] == identity['Account'] else {'Parents': [{'Id': 'r-test'}]} if params['ChildId'] == 'ou-test' else {}
            elif op == 'list-policies-for-target':
                return subprocess.CompletedProcess(cmd, 255, '', 'An error occurred (AccessDeniedException) when calling X')
            elif op == 'lookup-events':
                event = dict(eventSource='ec2.amazonaws.com', eventName='CreateVpc', userIdentity={'arn': identity['Arn'], 'principalId': identity['UserId']})
                other = dict(event, userIdentity={'arn': 'someone-else'})
                data = {'Events': [{'CloudTrailEvent': json.dumps(e)} for e in (event, other)]}
            else:
                data = {}
            return subprocess.CompletedProcess(cmd, 0, json.dumps(data), '')

        with tempfile.TemporaryDirectory() as tmp, patch.object(audit.subprocess, 'run', side_effect=fake):
            obj = audit.Audit(pathlib.Path(tmp) / 'report')
            requests = [dict(operation=op, region='us-east-1', scenario='volume-test', resource='test', parameters=params) for op, params in [('create-volume', {'AvailabilityZone': 'us-east-1a', 'Size': 1}), ('delete-volume', {'VolumeId': 'vol-test'})]]
            obj.collect(['us-east-1'], requests, history_days=1)
            archive = obj.reports()
            self.assertEqual(len(obj.history), 1)
            self.assertTrue(any(op == 'get-policy-version' and p['VersionId'] == 'v3' for _, op, p in calls))
            self.assertTrue(any(op == 'list-policies-for-target' and p['TargetId'] == 'r-test' for _, op, p in calls))
            risk = (obj.out / 'CREATE_BUT_CANNOT_DELETE.csv').read_text(encoding='utf-8-sig')
            self.assertIn('POTENTIAL_CREATE_WITHOUT_DELETE', risk)
            self.assertIn('iam:PassRole', (obj.out / 'UNKNOWN_ACTIONS.csv').read_text(encoding='utf-8-sig'))
            with zipfile.ZipFile(archive) as z:
                self.assertIsNone(z.testzip())
                self.assertTrue(any(n.endswith('.stdout.json') for n in z.namelist()))
            self.assertTrue(all(json.loads(p.read_text()) is not None for p in obj.out.glob('raw/*.stdout.json') if p.read_text()))

    def test_timeout_invalid_json_and_failure_are_unknown(self):
        with tempfile.TemporaryDirectory() as tmp:
            obj = audit.Audit(pathlib.Path(tmp) / 'report')
            with patch.object(audit.subprocess, 'run', side_effect=subprocess.TimeoutExpired(['aws'], 1)):
                obj.call('ec2', 'describe-vpcs')
            with patch.object(audit.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, 'invalid json', '')):
                obj.call('ec2', 'describe-subnets')
            self.assertEqual([r['Result'] for r in obj.rows], ['UNKNOWN', 'UNKNOWN'])
            with self.assertRaises(ValueError):
                obj.call('iam', 'create-role')
            with self.assertRaises(ValueError):
                obj.call('s3', 'delete-bucket', dry=True)
            # No total-report overwrite of prior evidence.
            with self.assertRaises(FileExistsError):
                audit.Audit(obj.out)

    def test_csv_formula_escaping_and_scope_comparison(self):
        with tempfile.TemporaryDirectory() as tmp:
            obj = audit.Audit(pathlib.Path(tmp) / 'report')
            obj.csv('safe.csv', [{'Resource': '=HYPERLINK("bad")'}], ['Resource'])
            with (obj.out / 'safe.csv').open(encoding='utf-8-sig') as f:
                self.assertTrue(next(csv.DictReader(f))['Resource'].startswith("'="))
            for op, result, scenario in [('create-volume', 'ALLOW', 'a'), ('delete-volume', 'DENY', 'b')]:
                obj.unknown(audit.action('ec2', op), 'us-east-1', 'volume', '')
                obj.rows[-1].update(Result=result, Scenario=scenario)
            obj.reports()
            with (obj.out / 'CREATE_BUT_CANNOT_DELETE.csv').open(encoding='utf-8-sig') as f:
                self.assertEqual(list(csv.DictReader(f)), [])
            self.assertIn('DELETE_UNVERIFIED', (obj.out / 'PERMISSION_ASYMMETRY.csv').read_text(encoding='utf-8-sig'))

    def test_user_group_and_managed_inline_policy_collection(self):
        with tempfile.TemporaryDirectory() as tmp:
            obj = audit.Audit(pathlib.Path(tmp) / 'report')
            def call(service, op, params=None, **kwargs):
                if op == 'list-groups-for-user':
                    return {'Groups': [{'GroupName': 'Developers'}]}
                if op.startswith('list-attached-'):
                    return {'AttachedPolicies': [{'PolicyArn': 'arn:policy/test'}]}
                if op in ('list-user-policies', 'list-group-policies'):
                    return {'PolicyNames': ['inline']}
                if op == 'get-policy':
                    return {'Policy': {'DefaultVersionId': 'v1'}}
                return {}
            with patch.object(obj, 'call', side_effect=call) as mocked:
                obj.principal_policies('user', 'me')
            names = [c.args[1] for c in mocked.call_args_list]
            for op in ('get-user-policy', 'get-group-policy', 'get-policy-version'):
                self.assertIn(op, names)


if __name__ == '__main__':
    unittest.main()
