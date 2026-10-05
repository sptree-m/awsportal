"""AWS boundary tests use fakes only; they never create cloud resources."""
import importlib.util
import json
import os
import pathlib
import sys
import types
import unittest
from unittest.mock import Mock, patch

boto = types.ModuleType('boto3')
boto.client = Mock()
errors = types.ModuleType('botocore.exceptions')
errors.ClientError = RuntimeError
with patch.dict(sys.modules, {'boto3': boto, 'botocore.exceptions': errors}):
    spec = importlib.util.spec_from_file_location('cloud_worker', pathlib.Path(__file__).parents[1] / 'workflows/cloud_control.py')
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)

class CloudWorkerTests(unittest.TestCase):
    def setUp(self):
        self.profile = {'ami_id':'ami-aaaaaaaa','launch_template_id':'lt-aaaaaaaa','launch_template_version':'1','ami_checksum':'a'*64}
        self.network = {'vpc_id':'vpc-test','subnet_id':'subnet-test','security_group_ids':['sg-test'],'allowed_ipv4_cidrs':['10.0.0.0/24'],'route_table_id':'rtb-test','transit_gateway_id':'tgw-test'}
        self.profile['network'] = self.network
        self.env = patch.dict(os.environ, {'APPROVED_POOLS':json.dumps({'1':self.profile}),'RESOURCE_CLASS':'shared','CREDENTIAL_PREFIX':'/test','KMS_KEY_ID':'test'})
        self.env.start();self.addCleanup(self.env.stop)
        self.ec2, self.ssm = Mock(), Mock()
        self.ssm.exceptions.ParameterNotFound = type('ParameterNotFound',(Exception,),{})
        self.ssm.exceptions.ParameterAlreadyExists = type('ParameterAlreadyExists',(Exception,),{})
        boto.client.side_effect = lambda name: self.ec2 if name == 'ec2' else self.ssm
        self.template = {'ImageId':'ami-aaaaaaaa','MetadataOptions':{'HttpTokens':'required'},'BlockDeviceMappings':[{'Ebs':{'Encrypted':True,'DeleteOnTermination':True,'VolumeSize':1024}}]}
        self.template['NetworkInterfaces'] = [{'DeviceIndex':0,'SubnetId':'subnet-test','Groups':['sg-test'],'AssociatePublicIpAddress':False}]
        self.ec2.describe_subnets.return_value = {'Subnets':[{'VpcId':'vpc-test','State':'available','CidrBlock':'10.0.0.0/24'}]}
        self.ec2.describe_security_groups.return_value = {'SecurityGroups':[{'VpcId':'vpc-test'}]}
        self.routes = [{'DestinationCidrBlock':'10.0.0.0/24','GatewayId':'local','State':'active'},{'DestinationCidrBlock':'0.0.0.0/0','TransitGatewayId':'tgw-test','State':'active'}]
        self.ec2.describe_route_tables.return_value = {'RouteTables':[{'RouteTableId':'rtb-test','VpcId':'vpc-test','Routes':self.routes}]}
        self.ec2.describe_launch_template_versions.return_value = {'LaunchTemplateVersions':[{'LaunchTemplateData':self.template}]}
        self.ec2.describe_images.return_value = {'Images':[{'State':'available','Tags':[{'Key':'awsportal:golden-sha256','Value':'a'*64}]}]}
        self.instance = {'InstanceId':'i-aaaaaaaa','PrivateIpAddress':'10.0.0.1','State':{'Name':'running'},'VpcId':'vpc-test','SubnetId':'subnet-test','SecurityGroups':[{'GroupId':'sg-test'}],'NetworkInterfaces':[{'NetworkInterfaceId':'eni-test'}],'Tags':[{'Key':'awsportal:'+k,'Value':v} for k,v in {'managed':'shared','pool':'1','generation':'1','operation':'b'*32}.items()],'BlockDeviceMappings':[{'Ebs':{'VolumeId':'vol-aaaaaaaa','DeleteOnTermination':True}}]}
        self.ec2.describe_instances.side_effect = lambda **kwargs: {'Reservations':[]} if 'Filters' in kwargs else {'Reservations':[{'Instances':[self.instance]}]}
        self.ec2.run_instances.return_value = {'Instances':[self.instance]}
        self.ssm.get_parameter.return_value = {'Parameter':{'Value':'c'*64}}
        self.event = {'operation_id':'b'*32,'environment_id':1,'generation':1,'kind':'PROVISION',**{'expected_'+k:v for k,v in {'image_id':'ami-aaaaaaaa','template_id':'lt-aaaaaaaa','template_version':'1','checksum':'a'*64}.items()}}
    def test_reconcile_keeps_original_client_token_and_approved_template(self):
        first=worker.handler(self.event,None)
        retry=worker.handler({**self.event,'retry_attempt':1},None)
        self.assertEqual(first,retry)
        for call in self.ec2.run_instances.call_args_list:
            self.assertEqual(call.kwargs['ClientToken'],'b'*32)
            self.assertEqual(call.kwargs['LaunchTemplate'],{'LaunchTemplateId':'lt-aaaaaaaa','Version':'1'})
        self.assertNotIn('credential',first)
        self.assertEqual(len(first['token_hash']),64)
    def test_tagged_original_is_adopted_without_launching(self):
        self.ec2.describe_instances.side_effect = lambda **kwargs: {'Reservations':[{'Instances':[self.instance]}]}
        result=worker.handler({**self.event,'retry_attempt':2},None)
        self.assertEqual(result['instance_id'],'i-aaaaaaaa')
        self.ec2.run_instances.assert_not_called()
        self.instance['State']['Name']='terminated'
        with self.assertRaises(RuntimeError):worker.handler(self.event,None)
        self.ec2.run_instances.assert_not_called()
    def test_duplicate_tagged_resources_are_quarantined(self):
        self.ec2.describe_instances.side_effect = lambda **kwargs: {'Reservations':[{'Instances':[self.instance,self.instance]}]}
        with self.assertRaises(RuntimeError):worker.handler(self.event,None)
        self.ec2.run_instances.assert_not_called()
    def test_changed_profile_never_launches(self):
        self.event['expected_image_id']='ami-bbbbbbbb'
        with self.assertRaises(ValueError):worker.handler(self.event,None)
        self.ec2.run_instances.assert_not_called()
    def test_unsafe_template_never_launches(self):
        for field,value in [('InstanceMarketOptions',{'MarketType':'spot'}),('MetadataOptions',{'HttpTokens':'optional'}),('BlockDeviceMappings',[{'Ebs':{'Encrypted':False,'DeleteOnTermination':True}}])]:
            with self.subTest(field=field):
                original=self.template.get(field);self.template[field]=value
                with self.assertRaises(ValueError):worker.handler(self.event,None)
                if original is None:self.template.pop(field)
                else:self.template[field]=original
        self.ec2.run_instances.assert_not_called()
    def test_private_network_failures_never_launch(self):
        for changed in [{'NatGatewayId':'nat-test'},{'GatewayId':'igw-test'},{'EgressOnlyInternetGatewayId':'eigw-test'},{'State':'blackhole'}]:
            with self.subTest(changed=changed):
                self.routes.append(changed)
                with self.assertRaises(ValueError):worker.handler(self.event,None)
                self.routes.pop()
        self.network['route_table_id']='rtb-other'
        # Environment JSON must match the edited profile.
        os.environ['APPROVED_POOLS']=json.dumps({'1':self.profile})
        with self.assertRaises(ValueError):worker.handler(self.event,None)
        self.ec2.run_instances.assert_not_called()
    def test_template_and_actual_network_mismatch_are_retained(self):
        self.template['NetworkInterfaces'][0]['AssociatePublicIpAddress']=True
        with self.assertRaises(ValueError):worker.handler(self.event,None)
        self.ec2.run_instances.assert_not_called()
        self.template['NetworkInterfaces'][0]['AssociatePublicIpAddress']=False
        self.instance['PublicIpAddress']='203.0.113.1'
        self.ec2.describe_instances.side_effect=lambda **kw: {'Reservations':[{'Instances':[self.instance]}]}
        with self.assertRaises(ValueError):worker.handler(self.event,None)
        self.ec2.run_instances.assert_not_called()
    def test_restricted_ip_allocation_is_pinned_and_exhaustion_fails(self):
        import ipaddress
        net={**self.network,'allowed_ipv4_cidrs':['10.0.0.8/30']}
        ranges=[ipaddress.IPv4Network('10.0.0.8/30')]
        subnet=ipaddress.IPv4Network('10.0.0.0/24')
        pinned={}
        def get(**kw):
            if not pinned:raise self.ssm.exceptions.ParameterNotFound()
            return {'Parameter':{'Value':pinned['value']}}
        def put(**kw):pinned['value']=kw['Value']
        self.ssm.get_parameter.side_effect=get
        self.ssm.put_parameter.side_effect=put
        self.ec2.describe_network_interfaces.return_value={'NetworkInterfaces':[{'PrivateIpAddresses':[{'PrivateIpAddress':'10.0.0.8'}]}]}
        first=worker.allocate_private_ip(self.ec2,self.ssm,'b'*32,net,subnet,ranges)
        second=worker.allocate_private_ip(self.ec2,self.ssm,'b'*32,net,subnet,ranges)
        self.assertEqual(first,second)
        self.assertIn(first,{'10.0.0.9','10.0.0.10','10.0.0.11'})
        self.ssm.put_parameter.assert_called_once()
        pinned.clear()
        self.ec2.describe_network_interfaces.return_value={'NetworkInterfaces':[{'PrivateIpAddresses':[{'PrivateIpAddress':str(i)} for i in ranges[0]]}]}
        with self.assertRaises(ValueError):worker.allocate_private_ip(self.ec2,self.ssm,'b'*32,net,subnet,ranges)
    def test_volume_modification_is_idempotent_and_waits_for_completed(self):
        event={**self.event,'kind':'MODIFY_VOLUME','instance_id':'i-aaaaaaaa','volume_id':'vol-aaaaaaaa','iops':3000,'throughput':125}
        volume={'Encrypted':True,'VolumeType':'gp3','Iops':16000,'Throughput':1000,'Tags':self.instance['Tags']}
        self.ec2.describe_volumes.return_value={'Volumes':[volume]}
        self.ec2.describe_volumes_modifications.return_value={'VolumesModifications':[]}
        self.assertTrue(worker.handler(event,None)['pending'])
        self.ec2.modify_volume.assert_called_once_with(VolumeId='vol-aaaaaaaa',VolumeType='gp3',Iops=3000,Throughput=125)
        volume.update(Iops=3000,Throughput=125)
        self.ec2.describe_volumes_modifications.return_value={'VolumesModifications':[{'ModificationState':'optimizing'}]}
        self.assertTrue(worker.handler(event,None)['pending'])
        self.ec2.modify_volume.assert_called_once()
        self.ec2.describe_volumes_modifications.return_value={'VolumesModifications':[{'ModificationState':'completed'}]}
        self.assertFalse(worker.handler(event,None)['pending'])
        self.ec2.modify_volume.assert_called_once()
    def test_volume_modification_rejects_unowned_or_persistent_volumes(self):
        event={**self.event,'kind':'MODIFY_VOLUME','instance_id':'i-aaaaaaaa','volume_id':'vol-other','iops':3000,'throughput':125}
        with self.assertRaises(ValueError):worker.handler(event,None)
        event['volume_id']='vol-aaaaaaaa'
        self.instance['BlockDeviceMappings'][0]['Ebs']['DeleteOnTermination']=False
        with self.assertRaises(ValueError):worker.handler(event,None)
        self.ec2.modify_volume.assert_not_called()
    def terminate(self):
        return worker.handler({**self.event,'kind':'TERMINATE','instance_id':'i-aaaaaaaa'},None)
    def test_termination_waits_for_ebs_before_revoking_credentials(self):
        self.assertTrue(self.terminate()['pending'])
        self.ssm.delete_parameter.assert_not_called()
        self.instance['State']['Name']='terminated'
        self.ec2.describe_volumes.return_value={'Volumes':[{'VolumeId':'vol-aaaaaaaa'}]}
        self.assertTrue(self.terminate()['pending'])
        self.ssm.delete_parameter.assert_not_called()
        self.ec2.describe_volumes.return_value={'Volumes':[]}
        result=self.terminate()
        self.assertTrue(result['resources_gone'] and result['credential_revoked'])
        self.ssm.delete_parameter.assert_any_call(Name='/test/i-aaaaaaaa/credential')
        self.ssm.delete_parameter.assert_any_call(Name='/test/operations/'+('b'*32)+'/network')
    def test_generation_mismatch_and_persistent_ebs_refuse_termination(self):
        self.instance['Tags'][2]['Value']='2'
        with self.assertRaises(ValueError):self.terminate()
        self.instance['Tags'][2]['Value']='1'
        self.instance['BlockDeviceMappings'][0]['Ebs']['DeleteOnTermination']=False
        with self.assertRaises(ValueError):self.terminate()
        self.ec2.terminate_instances.assert_not_called()

if __name__ == '__main__':unittest.main()
