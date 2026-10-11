#!/usr/bin/env python3
"""CloudShell inventory and scoped permission evidence. Standard library only."""
import argparse
import csv
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import zipfile

# Explicit allowlists: configuration never selects arbitrary AWS operations.
READS = {
    'ec2': 'describe-vpcs describe-subnets describe-route-tables describe-security-groups describe-network-acls describe-internet-gateways describe-nat-gateways describe-vpc-endpoints describe-vpc-peering-connections describe-transit-gateways describe-transit-gateway-attachments describe-transit-gateway-route-tables describe-vpn-gateways describe-customer-gateways describe-network-interfaces describe-addresses describe-managed-prefix-lists describe-instances describe-volumes describe-launch-templates describe-availability-zones',
    'efs': 'describe-file-systems', 'fsx': 'describe-file-systems',
    'autoscaling': 'describe-auto-scaling-groups', 'ecs': 'list-clusters',
    'eks': 'list-clusters', 'kms': 'list-keys',
    'ssm': 'describe-instance-information', 'cloudtrail': 'describe-trails',
    'configservice': 'describe-configuration-recorders', 'guardduty': 'list-detectors',
    'resourcegroupstaggingapi': 'get-resources',
    # ListStacks avoids collecting stack parameters that may contain credentials.
    'cloudformation': 'list-stacks',
}
DRY = {
    'Instance': ('run-instances', 'terminate-instances', 'start-instances', 'stop-instances', 'modify-instance-attribute'),
    'Volume': ('create-volume', 'delete-volume', 'attach-volume', 'detach-volume', 'modify-volume'),
    'VPC': ('create-vpc', 'delete-vpc'),
    'Subnet': ('create-subnet', 'delete-subnet'),
    'SecurityGroup': ('create-security-group', 'delete-security-group', 'authorize-security-group-ingress', 'revoke-security-group-ingress', 'authorize-security-group-egress', 'revoke-security-group-egress'),
    'RouteTable': ('create-route-table', 'delete-route-table', 'create-route', 'replace-route', 'delete-route', 'associate-route-table', 'disassociate-route-table'),
    'Snapshot': ('create-snapshot', 'delete-snapshot'),
    'Image': ('create-image', 'deregister-image'),
    'Tags': ('create-tags', 'delete-tags'),
}
OTHER = {
    'iam': 'CreateRole DeleteRole UpdateRole AttachRolePolicy DetachRolePolicy CreateInstanceProfile DeleteInstanceProfile AddRoleToInstanceProfile RemoveRoleFromInstanceProfile PassRole',
    'efs': 'CreateFileSystem DeleteFileSystem CreateMountTarget DeleteMountTarget CreateAccessPoint DeleteAccessPoint PutFileSystemPolicy',
    's3': 'CreateBucket DeleteBucket GetObject PutObject DeleteObject PutBucketPolicy',
    'kms': 'CreateKey ScheduleKeyDeletion Encrypt Decrypt GenerateDataKey CreateGrant',
    'cloudformation': 'CreateStack UpdateStack DeleteStack',
    'logs': 'CreateLogGroup DeleteLogGroup CreateLogStream PutLogEvents PutRetentionPolicy',
    'cloudwatch': 'PutMetricData GetMetricData',
    'ssm': 'SendCommand GetCommandInvocation',
    'ec2': 'ModifyVpcAttribute ModifySubnetAttribute CreateTransitGateway DeleteTransitGateway CreateTransitGatewayVpcAttachment DeleteTransitGatewayVpcAttachment',
}
PREFIX = {'efs': 'elasticfilesystem', 'configservice': 'config', 'resourcegroupstaggingapi': 'tag', 'service-quotas': 'servicequotas', 's3api': 's3'}
FIELDS = ['Action', 'Region', 'Scenario', 'Resource', 'Result', 'Method', 'ErrorCode', 'Evidence', 'RequestSHA256', 'ObservedAt', 'Note']
DENIED = {'AccessDenied', 'AccessDeniedException', 'UnauthorizedOperation', 'AuthorizationError', 'AuthorizationErrorException'}


def action(service, operation):
    if service == 's3api' and operation == 'list-buckets':
        return 's3:ListAllMyBuckets'
    return PREFIX.get(service, service) + ':' + ''.join(p[:1].upper() + p[1:] for p in operation.split('-'))


def classify(rc, err, dry=False):
    match = re.search(r'\(([^()]+)\) when calling', err)
    code = match.group(1) if match else ''
    if dry and code == 'DryRunOperation':
        return 'ALLOW', code
    if code in DENIED:
        return 'DENY', code
    if not dry and rc == 0:
        return 'ALLOW', code
    return 'UNKNOWN', code or ('UnexpectedSuccess' if dry and rc == 0 else 'CLI_ERROR')


def validate_requests(path, regions):
    if not path:
        return []
    data = json.loads(Path(path).read_text())
    if not isinstance(data, list):
        raise ValueError('DryRun config must be a JSON array')
    allowed = {op for ops in DRY.values() for op in ops}
    for req in data:
        if not isinstance(req, dict) or set(req) != {'operation', 'region', 'scenario', 'resource', 'parameters'}:
            raise ValueError('Each request needs operation, region, scenario, resource, parameters only')
        if req['operation'] not in allowed or req['region'] not in regions:
            raise ValueError('Unsupported operation or unselected region')
        if not all(isinstance(req[k], str) and req[k].strip() for k in ('scenario', 'resource')):
            raise ValueError('scenario and resource must be nonempty strings')
        if not isinstance(req['parameters'], dict):
            raise ValueError('parameters must be an object')
        if any(k.lower() == 'dryrun' for k in req['parameters']):
            raise ValueError('DryRun is enforced by the tool; do not specify it')
    return data


class Audit:
    def __init__(self, out, timeout=60):
        self.out = Path(out)
        self.out.mkdir(mode=0o700, parents=True, exist_ok=False)
        (self.out / 'raw').mkdir()
        self.timeout = timeout
        self.rows = []
        self.history = []
        self.identity = {}

    def call(self, service, operation, params=None, region='global', scenario='', resource='*', dry=False):
        params = dict(params or {})
        if dry:
            if service != 'ec2' or operation not in {op for ops in DRY.values() for op in ops}:
                raise ValueError('Unsafe DryRun operation')
            params['DryRun'] = True
        elif not (operation.startswith(('describe-', 'list-', 'get-')) or operation == 'lookup-events'):
            raise ValueError('Non-read operation requires enforced DryRun')
        digest = hashlib.sha256(json.dumps(params, sort_keys=True).encode()).hexdigest()
        stem = f'{len(self.rows):05d}-{service}-{operation}-{region}'
        base = self.out / 'raw' / stem
        cmd = ['aws', service, operation, '--output', 'json', '--no-cli-pager', '--cli-connect-timeout', '10', '--cli-read-timeout', '20']
        if region != 'global':
            cmd += ['--region', region]
        if params:
            cmd += ['--cli-input-json', json.dumps(params)]
        env = dict(os.environ, AWS_PAGER='', AWS_CLI_AUTO_PROMPT='off', AWS_MAX_ATTEMPTS='2')
        now = dt.datetime.now(dt.timezone.utc).isoformat()
        try:
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=self.timeout, env=env)
            rc, stdout, stderr = result.returncode, result.stdout, result.stderr
        except subprocess.TimeoutExpired:
            rc, stdout, stderr = 124, '', 'TIMEOUT: total command timeout (including pagination)'
        base.with_suffix('.stdout.json').write_text(stdout)
        base.with_suffix('.stderr.txt').write_text(stderr)
        base.with_suffix('.request.json').write_text(json.dumps({'command': cmd, 'timestamp': now, 'exit_code': rc}, indent=2))
        status, code = classify(rc, stderr, dry)
        obj = None
        if rc == 0 and not dry:
            try:
                obj = json.loads(stdout)
            except ValueError:
                status, code = 'UNKNOWN', 'INVALID_JSON'
        row = dict(zip(FIELDS, [action(service, operation), region, scenario, resource, status,
            'DRY_RUN' if dry else 'REAL_READ', code, f'raw/{stem}', digest, now,
            'この要求・時点のみ。実行成功や他リソースの権限は保証しない。' if dry else 'この読取要求のみ。']))
        self.rows.append(row)
        print(f"[{status}] {row['Action']} {region}", flush=True)
        return obj

    def managed_policy(self, arn):
        meta = self.call('iam', 'get-policy', {'PolicyArn': arn}, resource=arn)
        version = (meta or {}).get('Policy', {}).get('DefaultVersionId')
        if version:
            self.call('iam', 'get-policy-version', {'PolicyArn': arn, 'VersionId': version}, resource=arn)

    def principal_policies(self, kind, name):
        title = kind.title()
        params = {title + 'Name': name}
        meta = self.call('iam', 'get-' + kind, params, resource=name) if kind != 'group' else None
        attached = self.call('iam', 'list-attached-' + kind + '-policies', params, resource=name)
        for policy in (attached or {}).get('AttachedPolicies', []):
            self.managed_policy(policy['PolicyArn'])
        inline = self.call('iam', 'list-' + kind + '-policies', params, resource=name)
        for policy in (inline or {}).get('PolicyNames', []):
            self.call('iam', 'get-' + kind + '-policy', dict(params, PolicyName=policy), resource=name)
        boundary = (meta or {}).get(title, {}).get('PermissionsBoundary', {}).get('PermissionsBoundaryArn')
        if boundary:
            self.managed_policy(boundary)
        if kind == 'user':
            groups = self.call('iam', 'list-groups-for-user', params, resource=name)
            for group in (groups or {}).get('Groups', []):
                self.principal_policies('group', group['GroupName'])

    def organization(self, account):
        self.call('organizations', 'describe-organization')
        target, visited = account, set()
        while target and target not in visited:
            visited.add(target)
            for kind in ('SERVICE_CONTROL_POLICY', 'RESOURCE_CONTROL_POLICY'):
                policies = self.call('organizations', 'list-policies-for-target', {'TargetId': target, 'Filter': kind}, resource=target)
                for policy in (policies or {}).get('Policies', []):
                    self.call('organizations', 'describe-policy', {'PolicyId': policy['Id']}, resource=policy['Id'])
            if target.startswith('r-'):
                break
            parents = self.call('organizations', 'list-parents', {'ChildId': target}, resource=target)
            parent_list = (parents or {}).get('Parents', [])
            target = parent_list[0]['Id'] if parent_list else None

    def collect(self, regions, requests, history_days=0):
        self.identity = self.call('sts', 'get-caller-identity') or {}
        if not self.identity.get('Account') or not self.identity.get('Arn'):
            raise RuntimeError('Caller identity unavailable. Raw evidence retained; no further calls.')
        arn = self.identity['Arn']
        role = re.search(r':assumed-role/([^/]+)/', arn)
        if role:
            self.principal_policies('role', role.group(1))
        elif ':user/' in arn:
            self.principal_policies('user', arn.rsplit('/', 1)[1])
        self.organization(self.identity['Account'])
        self.call('iam', 'get-account-summary')
        self.call('s3api', 'list-buckets')
        self.call('ec2', 'describe-regions', {'AllRegions': True}, regions[0])
        for region in regions:
            for service, ops in READS.items():
                for op in ops.split():
                    self.call(service, op, region=region)
            self.call('ec2', 'describe-images', {'Owners': ['self']}, region)
            self.call('ec2', 'describe-snapshots', {'OwnerIds': ['self']}, region)
            for service in ('ec2', 'vpc'):
                self.call('service-quotas', 'list-service-quotas', {'ServiceCode': service}, region)
            if history_days:
                start = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=history_days)
                events = self.call('cloudtrail', 'lookup-events', {'StartTime': start.isoformat()}, region)
                for entry in (events or {}).get('Events', []):
                    try:
                        event = json.loads(entry['CloudTrailEvent'])
                    except (ValueError, KeyError):
                        continue
                    who = event.get('userIdentity', {})
                    # Only this exact principal/session; do not attribute other users' calls.
                    if who.get('arn') != arn or who.get('principalId') != self.identity.get('UserId'):
                        continue
                    self.history.append({'Region': region, 'EventTime': event.get('eventTime', ''),
                        'EventSource': event.get('eventSource', ''), 'EventName': event.get('eventName', ''),
                        'Result': 'HISTORICAL_ERROR' if event.get('errorCode') else 'HISTORICAL_SUCCESS',
                        'ErrorCode': event.get('errorCode', ''), 'EventId': event.get('eventID', ''),
                        'Note': '過去の要求結果。現在の権限判定には使用しない。'})
        for req in requests:
            self.call('ec2', req['operation'], req['parameters'], req['region'], req['scenario'], req['resource'], dry=True)
        # Exhaustive only for this documented catalog, never for all AWS actions.
        tested = {(r['Action'], r['Region']) for r in self.rows}
        for region in regions:
            for group, ops in DRY.items():
                for op in ops:
                    a = action('ec2', op)
                    if (a, region) not in tested:
                        self.unknown(a, region, group, '有効な対象・要求が未指定')
            for service, names in OTHER.items():
                for name in names.split():
                    self.unknown(PREFIX.get(service, service) + ':' + name, region, service, '安全なDryRun/Simulatorなし')

    def unknown(self, a, region, resource, note):
        self.rows.append(dict(zip(FIELDS, [a, region, '', resource, 'UNKNOWN', 'NO_SAFE_TEST', '', '', '', '', note])))

    def csv(self, name, rows, fields=FIELDS):
        with (self.out / name).open('w', newline='', encoding='utf-8-sig') as f:
            writer = csv.DictWriter(f, fieldnames=fields)
            writer.writeheader()
            # Prevent spreadsheet formula execution for resource names or errors.
            writer.writerows({k: "'" + str(v) if str(v).startswith(('=', '+', '-', '@', '\t', '\r', '\n')) else v for k, v in row.items()} for row in rows)

    def reports(self):
        self.csv('PERMISSION_MATRIX.csv', self.rows)
        for name, status in [('DENIED_ACTIONS', 'DENY'), ('UNKNOWN_ACTIONS', 'UNKNOWN')]:
            self.csv(name + '.csv', [r for r in self.rows if r['Result'] == status])
        self.csv('DRYRUN_RESULTS.csv', [r for r in self.rows if r['Method'] == 'DRY_RUN'])
        self.csv('POLICY_VISIBILITY.csv', [r for r in self.rows if r['Action'].split(':')[0] in ('iam', 'organizations') and r['Method'] == 'REAL_READ'])
        self.csv('AWSPORTAL_REQUIREMENTS.csv', [r for r in self.rows if r['Action'].split(':')[0] in ('ec2', 'iam', 'elasticfilesystem', 's3', 'cloudformation', 'ssm', 'logs', 'cloudwatch')])
        self.csv('CLOUDTRAIL_PERMISSION_HISTORY.csv', self.history, ['Region', 'EventTime', 'EventSource', 'EventName', 'Result', 'ErrorCode', 'EventId', 'Note'])
        pairs = []
        lifecycles = [(group, action('ec2', ops[0]), action('ec2', ops[1])) for group, ops in DRY.items()]
        lifecycles += [(group, PREFIX.get(service, service) + ':' + create, PREFIX.get(service, service) + ':' + delete) for service, group, create, delete in [
            ('iam', 'Role', 'CreateRole', 'DeleteRole'),
            ('iam', 'InstanceProfile', 'CreateInstanceProfile', 'DeleteInstanceProfile'),
            ('efs', 'FileSystem', 'CreateFileSystem', 'DeleteFileSystem'),
            ('efs', 'MountTarget', 'CreateMountTarget', 'DeleteMountTarget'),
            ('efs', 'AccessPoint', 'CreateAccessPoint', 'DeleteAccessPoint'),
            ('s3', 'Bucket', 'CreateBucket', 'DeleteBucket'),
            ('cloudformation', 'Stack', 'CreateStack', 'DeleteStack'),
            ('logs', 'LogGroup', 'CreateLogGroup', 'DeleteLogGroup'),
        ]]
        for group, create, delete in lifecycles:
            for cr in self.rows:
                if cr['Action'] != create:
                    continue
                deletes = [r for r in self.rows if r['Action'] == delete and r['Region'] == cr['Region'] and r['Scenario'] == cr['Scenario']]
                if not deletes:
                    deletes = [{'Resource': '', 'Result': 'UNKNOWN', 'Evidence': ''}]
                for dr in deletes:
                    flag = 'POTENTIAL_CREATE_WITHOUT_DELETE' if cr['Result'] == 'ALLOW' and dr['Result'] == 'DENY' else 'DELETE_UNVERIFIED' if cr['Result'] == 'ALLOW' and dr['Result'] == 'UNKNOWN' else 'SCOPED_RESULTS_ONLY'
                    pairs.append({'ResourceType': group, 'Region': cr['Region'], 'Scenario': cr['Scenario'],
                        'CreateAction': create, 'Create': cr['Result'], 'CreateResource': cr['Resource'], 'CreateEvidence': cr['Evidence'],
                        'DeleteAction': delete, 'Delete': dr['Result'], 'DeleteResource': dr['Resource'], 'DeleteEvidence': dr['Evidence'],
                        'Finding': flag, 'Note': '別要求の比較。新規作成リソースの削除可否を保証しない。'})
        fields = ['ResourceType', 'Region', 'Scenario', 'CreateAction', 'Create', 'CreateResource', 'CreateEvidence', 'DeleteAction', 'Delete', 'DeleteResource', 'DeleteEvidence', 'Finding', 'Note']
        self.csv('PERMISSION_ASYMMETRY.csv', pairs, fields)
        self.csv('CREATE_BUT_CANNOT_DELETE.csv', [r for r in pairs if r['Finding'] == 'POTENTIAL_CREATE_WITHOUT_DELETE'], fields)
        summary = {'identity': self.identity, 'results': {s: sum(r['Result'] == s for r in self.rows) for s in ('ALLOW', 'DENY', 'UNKNOWN')}}
        (self.out / 'ENVIRONMENT_SUMMARY.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2))
        (self.out / 'SUMMARY.txt').write_text('AWS environment audit\n' + json.dumps(summary, ensure_ascii=False, indent=2) + '\n完全な実効権限一覧ではありません。ALLOW/DENYは記録した要求・時点・対象のみ。\nUNKNOWNには未指定対象、未対応操作、通信・構文・サービスエラーを含みます。\nCloudTrail履歴とPolicy文書は現在の実効権限へ変換しません。\nCREATE_BUT_CANNOT_DELETEは潜在的な非対称性であり、新規リソースの削除可否は未保証。\n秘密値・VPN構成(PSK)・EC2 user-dataは取得しません。構成/Policy情報は含みます。\n')
        archive = self.out.with_name(self.out.name + '.zip')
        with zipfile.ZipFile(archive, 'x', zipfile.ZIP_DEFLATED) as z:
            for file in sorted(self.out.rglob('*')):
                if file.is_file():
                    z.write(file, file.relative_to(self.out.parent))
        print(f'\nDownload path: {archive.resolve()}')
        return archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--regions', nargs='+', help='Explicit region list; defaults to CloudShell current region')
    parser.add_argument('--all-regions', action='store_true', help='Scan all enabled EC2 regions')
    parser.add_argument('--dry-run-config', help='Explicit EC2 requests (all forcibly DryRun=true)')
    parser.add_argument('--cloudtrail-days', type=int, default=0, choices=range(0, 91), metavar='0..90')
    parser.add_argument('--timeout', type=int, default=60, help='Total seconds per AWS CLI call including pagination')
    parser.add_argument('--output-dir', help='New directory; default ~/aws-environment-audit-TIMESTAMP')
    args = parser.parse_args()
    if args.timeout <= 0 or (args.regions and args.all_regions):
        parser.error('timeout must be positive; choose regions OR all-regions')
    if not shutil.which('aws'):
        parser.error('AWS CLI is required (available in CloudShell)')
    os.umask(0o077)
    regions = args.regions or [os.getenv('AWS_REGION') or os.getenv('AWS_DEFAULT_REGION') or 'us-east-1']
    out = args.output_dir or str(Path.home() / ('aws-environment-audit-' + dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')))
    audit = Audit(out, args.timeout)
    try:
        if args.all_regions:
            result = audit.call('ec2', 'describe-regions', region=regions[0])
            if not result:
                raise RuntimeError('Enabled region discovery failed; specify --regions')
            regions = [r['RegionName'] for r in result.get('Regions', []) if r.get('OptInStatus') != 'not-opted-in']
            if not regions:
                raise RuntimeError('No enabled regions returned')
        requests = validate_requests(args.dry_run_config, regions)
        audit.collect(regions, requests, args.cloudtrail_days)
        audit.reports()
        return 0
    except (ValueError, RuntimeError, OSError) as e:
        print(f'ERROR: {e}\nEvidence: {audit.out}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
