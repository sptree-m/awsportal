"""Approved-profile AWS worker. No request may choose an AMI, IAM role or SG.
Deploy separate copies/roles for Shared and Windows Import. No Box API calls.
"""
import hashlib
import json
import os
import secrets
import boto3
from botocore.exceptions import ClientError

def handler(event, context):
    operation = event['operation_id']
    if len(operation) != 32 or any(c not in '0123456789abcdef' for c in operation):
        raise ValueError('invalid operation identity')
    kind, pool, generation = event['kind'], str(event['environment_id']), str(event['generation'])
    approved = json.loads(os.environ['APPROVED_POOLS'])
    if pool not in approved:
        raise ValueError('pool not approved')
    profile = approved[pool]
    ec2, ssm = boto3.client('ec2'), boto3.client('ssm')
    if kind == 'PROVISION':
        if any(event.get(k) != profile[v] for k, v in (
            ('expected_image_id', 'ami_id'), ('expected_template_id', 'launch_template_id'),
            ('expected_template_version', 'launch_template_version'), ('expected_checksum', 'ami_checksum'))):
            raise ValueError('Stable registry and deployed approved profile disagree')
        template = ec2.describe_launch_template_versions(LaunchTemplateId=profile['launch_template_id'],
            Versions=[profile['launch_template_version']])['LaunchTemplateVersions'][0]['LaunchTemplateData']
        mappings=[b['Ebs'] for b in template.get('BlockDeviceMappings', []) if 'Ebs' in b]
        if not mappings or any(not b.get('Encrypted') or not b.get('DeleteOnTermination') for b in mappings):
            raise ValueError('encrypted transient EBS mappings required')
        if os.environ['RESOURCE_CLASS']=='windows-import' and not any(b.get('VolumeSize',0)>=1024 for b in mappings):
            raise ValueError('Windows offline import requires 1 TiB capacity')
        if template['ImageId'] != profile['ami_id'] or template.get('InstanceMarketOptions'):
            raise ValueError('approved on-demand Golden image required')
        if template.get('MetadataOptions', {}).get('HttpTokens') != 'required':
            raise ValueError('IMDSv2 required')
        image = ec2.describe_images(ImageIds=[profile['ami_id']])['Images'][0]
        image_tags = {t['Key']:t['Value'] for t in image.get('Tags', [])}
        if image['State'] != 'available' or image_tags.get('awsportal:golden-sha256') != profile['ami_checksum']:
            raise ValueError('Golden image checksum attestation mismatch')
        # Reconcile tagged resources before any launch, including after a long outage.
        # ClientToken retry alone must not invent another resource for the same slot.
        known = ec2.describe_instances(Filters=[
            {'Name':'tag:awsportal:operation','Values':[operation]},
            {'Name':'tag:awsportal:managed','Values':[os.environ['RESOURCE_CLASS']]},
            {'Name':'tag:awsportal:pool','Values':[pool]},
            {'Name':'tag:awsportal:generation','Values':[generation]}])
        candidates = [i for r in known['Reservations'] for i in r['Instances']]
        if len(candidates) > 1:
            raise RuntimeError('multiple resources for one reservation; quarantine and investigate')
        if candidates:
            instance = candidates[0]
            iid = instance['InstanceId']
        else:
            response = ec2.run_instances(MinCount=1, MaxCount=1, ClientToken=operation,
                LaunchTemplate={'LaunchTemplateId': profile['launch_template_id'], 'Version': profile['launch_template_version']},
                TagSpecifications=[{'ResourceType': typ, 'Tags': [
                    {'Key': 'awsportal:managed', 'Value': os.environ['RESOURCE_CLASS']},
                    {'Key': 'awsportal:pool', 'Value': pool},
                    {'Key': 'awsportal:generation', 'Value': generation},
                    {'Key': 'awsportal:operation', 'Value': operation}]} for typ in ('instance', 'volume')])
            iid = response['Instances'][0]['InstanceId']
            instance = ec2.describe_instances(InstanceIds=[iid])['Reservations'][0]['Instances'][0]
        if instance['State']['Name'] in ('terminated', 'shutting-down'):
            raise RuntimeError('provisioned resource terminated; reconciliation required')
        if instance['State']['Name'] != 'running':
            return {'pending': True}
        parameter = f"{os.environ['CREDENTIAL_PREFIX']}/{iid}/credential"
        try:
            token = ssm.get_parameter(Name=parameter, WithDecryption=True)['Parameter']['Value']
        except ssm.exceptions.ParameterNotFound:
            token = secrets.token_hex(32)
            try:
                ssm.put_parameter(Name=parameter, Type='SecureString', KeyId=os.environ['KMS_KEY_ID'],
                    Value=token, Overwrite=False, Tags=[{'Key': 'awsportal:operation', 'Value': operation}])
            except ssm.exceptions.ParameterAlreadyExists:
                token = ssm.get_parameter(Name=parameter, WithDecryption=True)['Parameter']['Value']
        if os.environ['RESOURCE_CLASS'] == 'windows-import':
            admin_parameter = f"{os.environ['CREDENTIAL_PREFIX']}/{iid}/windows-admin"
            try:
                ssm.put_parameter(Name=admin_parameter, Type='SecureString', KeyId=os.environ['KMS_KEY_ID'],
                    Value=json.dumps({'username':'AwsImportAdmin','password':'Aa1!'+secrets.token_hex(24)}),Overwrite=False)
            except ssm.exceptions.ParameterAlreadyExists:
                pass
        return {'pending': False, 'instance_id': iid, 'host': instance['PrivateIpAddress'],
                'token_hash': hashlib.sha256(token.encode()).hexdigest(),
                'volume_ids': [b['Ebs']['VolumeId'] for b in instance.get('BlockDeviceMappings', []) if 'Ebs' in b]}
    if kind != 'TERMINATE':
        raise ValueError('unknown action')
    iid = event['instance_id']
    # A credential for this instance is revoked only after proof-bound termination.
    # The caller is the durable controller; IAM restricts StartExecution separately.
    result = ec2.describe_instances(InstanceIds=[iid])
    instances = [i for r in result['Reservations'] for i in r['Instances']]
    if not instances:
        raise RuntimeError('missing resource identity; explicit reconciliation required')
    instance = instances[0]
    tags = {t['Key']: t['Value'] for t in instance.get('Tags', [])}
    if (tags.get('awsportal:managed') != os.environ['RESOURCE_CLASS'] or
        tags.get('awsportal:pool') != pool or tags.get('awsportal:generation') != generation):
        raise ValueError('resource ownership/generation mismatch')
    if instance['State']['Name'] != 'terminated':
        # EBS must be transient. Refuse deletion if operator changed the profile.
        if any(not b['Ebs'].get('DeleteOnTermination', False) for b in instance.get('BlockDeviceMappings', []) if 'Ebs' in b):
            raise ValueError('persistent EBS attached; terminate refused')
        ec2.terminate_instances(InstanceIds=[iid])
        return {'pending': True}
    # Tagged EBS includes resources that disappear from BlockDeviceMappings.
    volumes = ec2.describe_volumes(Filters=[{'Name':'tag:awsportal:operation','Values':[tags['awsportal:operation']]}])['Volumes']
    if volumes:
        return {'pending': True}
    parameter = f"{os.environ['CREDENTIAL_PREFIX']}/{iid}/credential"
    try:
        ssm.delete_parameter(Name=parameter)
    except ssm.exceptions.ParameterNotFound:
        pass
    if os.environ['RESOURCE_CLASS'] == 'windows-import':
        try:
            ssm.delete_parameter(Name=f"{os.environ['CREDENTIAL_PREFIX']}/{iid}/windows-admin")
        except ssm.exceptions.ParameterNotFound:
            pass
    return {'pending': False, 'resources_gone': True, 'credential_revoked': True}
