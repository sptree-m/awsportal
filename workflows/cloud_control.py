"""Approved-profile AWS worker. No request may choose an AMI, IAM role or SG.
Deploy separate copies/roles for Shared and Windows Import. No Box API calls.
"""
import hashlib
import ipaddress
import copy
import json
import os
import secrets
import boto3
from botocore.exceptions import ClientError

def network_contract(ec2, profile, template):
    """Read-only checks of approved VPC, IP ranges and effective subnet routes.
    TGW acceptance, return routes, NACLs, DNS and firewall rules require live tests.
    Never edit an organization-owned network to make these checks pass.
    """
    network = profile.get('network')
    if not network:
        raise ValueError('approved network contract required; re-register deployment profile')
    interfaces = template.get('NetworkInterfaces', [])
    if len(interfaces) != 1 or interfaces[0].get('DeviceIndex') != 0:
        raise ValueError('one private primary interface required')
    interface = interfaces[0]
    if (interface.get('SubnetId') != network['subnet_id'] or
        set(interface.get('Groups', [])) != set(network['security_group_ids']) or
        interface.get('AssociatePublicIpAddress') is not False or
        interface.get('NetworkInterfaceId') or interface.get('PrivateIpAddress') or
        interface.get('PrivateIpAddresses') or interface.get('Ipv6Addresses') or
        interface.get('Ipv6AddressCount', 0) or interface.get('Ipv6Prefixes') or interface.get('Ipv6PrefixCount', 0) or template.get('SecurityGroupIds')):
        raise ValueError('launch template differs from approved private network')
    subnet = ec2.describe_subnets(SubnetIds=[network['subnet_id']])['Subnets'][0]
    if subnet['VpcId'] != network['vpc_id'] or subnet['State'] != 'available' or subnet.get('AssignIpv6AddressOnCreation'):
        raise ValueError('subnet/VPC unavailable or mismatched')
    subnet_cidr = ipaddress.IPv4Network(subnet['CidrBlock'])
    ranges = [ipaddress.IPv4Network(c) for c in network['allowed_ipv4_cidrs']]
    if not ranges or any(not r.subnet_of(subnet_cidr) for r in ranges):
        raise ValueError('approved IPv4 ranges must fit the subnet')
    auto = len(ranges) == 1 and ranges[0] == subnet_cidr
    if not auto and sum(r.num_addresses for r in ranges) > 4096:
        raise ValueError('restricted IP pool may contain at most 4096 addresses')
    groups = ec2.describe_security_groups(GroupIds=network['security_group_ids'])['SecurityGroups']
    if not groups or any(g['VpcId'] != network['vpc_id'] for g in groups):
        raise ValueError('security group/VPC mismatch')
    tables = ec2.describe_route_tables(Filters=[{'Name':'association.subnet-id','Values':[network['subnet_id']]}])['RouteTables']
    if not tables:
        tables = ec2.describe_route_tables(Filters=[{'Name':'vpc-id','Values':[network['vpc_id']]}, {'Name':'association.main','Values':['true']}])['RouteTables']
    if len(tables) != 1 or tables[0]['RouteTableId'] != network['route_table_id'] or tables[0]['VpcId'] != network['vpc_id']:
        raise ValueError('effective route table differs from approved profile')
    routes = tables[0]['Routes']
    if any(r.get('State') == 'blackhole' for r in routes):
        raise ValueError('blackhole route requires network administrator repair')
    if any(r.get('NatGatewayId') or r.get('GatewayId','').startswith('igw-') or r.get('EgressOnlyInternetGatewayId') for r in routes):
        raise ValueError('direct Internet/NAT routes are forbidden in private profiles')
    tgw = network.get('transit_gateway_id', '')
    if tgw and not any(r.get('TransitGatewayId') == tgw for r in routes):
        raise ValueError('approved TGW route missing')
    if any(r.get('TransitGatewayId') and r['TransitGatewayId'] != tgw for r in routes):
        raise ValueError('unapproved TGW route')
    return subnet_cidr, ranges, auto

def allocate_private_ip(ec2, ssm, operation, network, subnet_cidr, ranges):
    # Pin the choice BEFORE RunInstances. A retry must never change its IP or
    # idempotency input, even if another account takes the selected address.
    parameter = f"{os.environ['CREDENTIAL_PREFIX']}/operations/{operation}/network"
    try:
        value = json.loads(ssm.get_parameter(Name=parameter, WithDecryption=True)['Parameter']['Value'])
    except ssm.exceptions.ParameterNotFound:
        used = set()
        kwargs = {'Filters':[{'Name':'subnet-id','Values':[network['subnet_id']]}]}
        while True:
            page = ec2.describe_network_interfaces(**kwargs)
            used.update(p['PrivateIpAddress'] for nic in page['NetworkInterfaces'] for p in nic.get('PrivateIpAddresses', []))
            if not page.get('NextToken'): break
            kwargs['NextToken'] = page['NextToken']
        # AWS reserves the first four and last IP of each subnet, not each pool.
        addresses = sorted({int(a) for r in ranges for a in r if int(a) >= int(subnet_cidr.network_address)+4 and a != subnet_cidr.broadcast_address})
        offset = int(operation, 16) % max(1, len(addresses))
        addresses = addresses[offset:] + addresses[:offset]
        selected = next((str(ipaddress.IPv4Address(a)) for a in addresses if str(ipaddress.IPv4Address(a)) not in used), None)
        if not selected: raise ValueError('approved private IPv4 pool exhausted')
        value = {'subnet_id':network['subnet_id'], 'private_ip':selected}
        try:
            ssm.put_parameter(Name=parameter, Type='SecureString', KeyId=os.environ['KMS_KEY_ID'], Value=json.dumps(value, sort_keys=True), Overwrite=False)
        except ssm.exceptions.ParameterAlreadyExists:
            value = json.loads(ssm.get_parameter(Name=parameter, WithDecryption=True)['Parameter']['Value'])
    address = ipaddress.IPv4Address(value['private_ip'])
    if value['subnet_id'] != network['subnet_id'] or int(address) < int(subnet_cidr.network_address)+4 or address == subnet_cidr.broadcast_address or not any(address in r for r in ranges):
        raise ValueError('pinned private IPv4 allocation differs from approved profile')
    return value['private_ip']

def verify_instance_network(instance, network):
    address = ipaddress.IPv4Address(instance['PrivateIpAddress'])
    if (instance.get('VpcId') != network['vpc_id'] or instance.get('SubnetId') != network['subnet_id'] or
        instance.get('PublicIpAddress') or set(g['GroupId'] for g in instance.get('SecurityGroups', [])) != set(network['security_group_ids']) or
        not any(address in ipaddress.IPv4Network(c) for c in network['allowed_ipv4_cidrs']) or
        len(instance.get('NetworkInterfaces', [])) != 1 or any(n.get('Ipv6Addresses') or n.get('Ipv6Prefixes') or n.get('Association',{}).get('PublicIp') for n in instance.get('NetworkInterfaces', []))):
        raise ValueError('actual instance violates approved network; retain and investigate')

def modify_volume(ec2, event, pool, generation):
    if os.environ['RESOURCE_CLASS'] != 'shared':
        raise ValueError('volume performance changes are Shared only')
    volume_id = event['volume_id']
    iops, throughput = event['iops'], event['throughput']
    if (type(iops) is not int or type(throughput) is not int or
        not 3000 <= iops <= 16000 or not 125 <= throughput <= 1000 or throughput*4 > iops):
        raise ValueError('approved gp3 performance range required')
    instance = ec2.describe_instances(InstanceIds=[event['instance_id']])['Reservations'][0]['Instances'][0]
    tags = {t['Key']:t['Value'] for t in instance.get('Tags', [])}
    if (tags.get('awsportal:managed') != 'shared' or tags.get('awsportal:pool') != pool or
        tags.get('awsportal:generation') != generation or instance['State']['Name'] != 'running' or
        not any(b.get('Ebs',{}).get('VolumeId') == volume_id and b['Ebs'].get('DeleteOnTermination') for b in instance.get('BlockDeviceMappings', []))):
        raise ValueError('managed running transient volume required')
    volume = ec2.describe_volumes(VolumeIds=[volume_id])['Volumes'][0]
    volume_tags = {t['Key']:t['Value'] for t in volume.get('Tags', [])}
    if (not volume.get('Encrypted') or volume.get('VolumeType') != 'gp3' or
        volume_tags.get('awsportal:managed') != 'shared' or volume_tags.get('awsportal:operation') != tags.get('awsportal:operation')):
        raise ValueError('encrypted owned gp3 required')
    changes = ec2.describe_volumes_modifications(VolumeIds=[volume_id])['VolumesModifications']
    matches = volume.get('Iops') == iops and volume.get('Throughput') == throughput
    if changes and changes[0]['ModificationState'] in ('modifying', 'optimizing'):
        # Do not overwrite another modification or declare an optimizing volume done.
        return {'pending':True}
    if matches:
        return {'pending':False, 'volume_id':volume_id, 'iops':iops, 'throughput':throughput}
    if changes and changes[0]['ModificationState'] != 'completed':
        raise ValueError('previous volume modification failed; manual repair required')
    # AWS enforces the rolling modification quota. Do not assume a six-hour rule.
    ec2.modify_volume(VolumeId=volume_id, VolumeType='gp3', Iops=iops, Throughput=throughput)
    return {'pending':True}

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
    if kind == 'MODIFY_VOLUME':
        return modify_volume(ec2, event, pool, generation)
    if kind == 'PROVISION':
        if any(event.get(k) != profile[v] for k, v in (
            ('expected_image_id', 'ami_id'), ('expected_template_id', 'launch_template_id'),
            ('expected_template_version', 'launch_template_version'), ('expected_checksum', 'ami_checksum'))):
            raise ValueError('Stable registry and deployed approved profile disagree')
        template = ec2.describe_launch_template_versions(LaunchTemplateId=profile['launch_template_id'],
            Versions=[profile['launch_template_version']])['LaunchTemplateVersions'][0]['LaunchTemplateData']
        subnet_cidr, ranges, auto_ip = network_contract(ec2, profile, template)
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
            override = {}
            if not auto_ip:
                interface = copy.deepcopy(template['NetworkInterfaces'][0])
                interface['PrivateIpAddress'] = allocate_private_ip(ec2, ssm, operation, profile['network'], subnet_cidr, ranges)
                override['NetworkInterfaces'] = [interface]
            response = ec2.run_instances(MinCount=1, MaxCount=1, ClientToken=operation,
                LaunchTemplate={'LaunchTemplateId': profile['launch_template_id'], 'Version': profile['launch_template_version']},
                TagSpecifications=[{'ResourceType': typ, 'Tags': [
                    {'Key': 'awsportal:managed', 'Value': os.environ['RESOURCE_CLASS']},
                    {'Key': 'awsportal:pool', 'Value': pool},
                    {'Key': 'awsportal:generation', 'Value': generation},
                    {'Key': 'awsportal:operation', 'Value': operation}]} for typ in ('instance', 'volume')], **override)
            iid = response['Instances'][0]['InstanceId']
            instance = ec2.describe_instances(InstanceIds=[iid])['Reservations'][0]['Instances'][0]
        verify_instance_network(instance, profile['network'])
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
    original = tags.get('awsportal:operation', '')
    if len(original) != 32 or any(c not in '0123456789abcdef' for c in original):
        raise ValueError('invalid original allocation identity')
    try:
        ssm.delete_parameter(Name=f"{os.environ['CREDENTIAL_PREFIX']}/operations/{original}/network")
    except ssm.exceptions.ParameterNotFound:
        pass
    return {'pending': False, 'resources_gone': True, 'credential_revoked': True}
