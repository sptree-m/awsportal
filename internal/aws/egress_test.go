package awsapi

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/sptree-m/awsportal/internal/store"
	"testing"
)

type fakeEgress struct {
	instance   types.Instance
	group      types.SecurityGroup
	interfaces []types.NetworkInterface
	events     []string
	fail       bool
}

func (f *fakeEgress) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: []types.Instance{f.instance}}}}, nil
}
func (f *fakeEgress) DescribeNetworkInterfaces(context.Context, *ec2.DescribeNetworkInterfacesInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error) {
	return &ec2.DescribeNetworkInterfacesOutput{NetworkInterfaces: f.interfaces}, nil
}
func (f *fakeEgress) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []types.SecurityGroup{f.group, {GroupId: aws.String("sg-proxy"), VpcId: aws.String("vpc-test")}}}, nil
}
func (f *fakeEgress) RevokeSecurityGroupEgress(context.Context, *ec2.RevokeSecurityGroupEgressInput, ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	f.events = append(f.events, "revoke")
	f.group.IpPermissionsEgress = nil
	return &ec2.RevokeSecurityGroupEgressOutput{}, nil
}
func (f *fakeEgress) AuthorizeSecurityGroupEgress(_ context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	f.events = append(f.events, "authorize")
	if f.fail {
		return nil, fmt.Errorf("quota")
	}
	f.group.IpPermissionsEgress = in.IpPermissions
	return &ec2.AuthorizeSecurityGroupEgressOutput{}, nil
}
func egressFixture() (*Egress, *fakeEgress, store.EgressPolicy) {
	ni := types.InstanceNetworkInterface{NetworkInterfaceId: aws.String("eni-one"), Groups: []types.GroupIdentifier{{GroupId: aws.String("sg-managed")}}}
	f := &fakeEgress{}
	f.instance = types.Instance{InstanceId: aws.String("i-test"), VpcId: aws.String("vpc-test"), NetworkInterfaces: []types.InstanceNetworkInterface{ni}}
	f.interfaces = []types.NetworkInterface{{NetworkInterfaceId: aws.String("eni-one")}}
	f.group = types.SecurityGroup{GroupId: aws.String("sg-managed"), VpcId: aws.String("vpc-test"), Tags: []types.Tag{{Key: aws.String("awsportal:egress-instance"), Value: aws.String("i-test")}}}
	f.group.IpPermissions = []types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(8443), ToPort: aws.Int32(8443)}}
	f.group.IpPermissionsEgress = []types.IpPermission{{IpProtocol: aws.String("-1"), IpRanges: []types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}}}
	p := store.EgressPolicy{InstanceID: "i-test", SecurityGroupID: "sg-managed", ProxyGroupID: "sg-proxy", ProxyPort: 3128, Rules: []store.EgressRule{{CIDR: "10.0.0.0/16", Protocol: "tcp", From: 443, To: 443}, {CIDR: "2001:db8::/64", Protocol: "udp", From: 123, To: 123}}}
	return &Egress{f}, f, p
}
func TestApplyEgressAndFailClosed(t *testing.T) {
	e, f, p := egressFixture()
	if err := e.ApplyEgress(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.events) != "[revoke authorize]" {
		t.Fatal(f.events)
	}
	if len(f.group.IpPermissions) != 1 || len(f.group.IpPermissionsEgress) != 3 {
		t.Fatal("ingress changed or wrong outbound")
	}
	if err := e.ApplyEgress(context.Background(), p); err != nil || len(f.events) != 2 {
		t.Fatal("non-idempotent", err)
	}
	e, f, p = egressFixture()
	f.fail = true
	if e.ApplyEgress(context.Background(), p) == nil || len(f.group.IpPermissionsEgress) != 0 {
		t.Fatal("failure reopened outbound")
	}
}
func TestRejectUnsafeEgressTargets(t *testing.T) {
	for _, mutate := range []func(*fakeEgress){func(f *fakeEgress) {
		f.instance.NetworkInterfaces = append(f.instance.NetworkInterfaces, f.instance.NetworkInterfaces[0])
	}, func(f *fakeEgress) {
		f.instance.NetworkInterfaces[0].Groups = append(f.instance.NetworkInterfaces[0].Groups, types.GroupIdentifier{GroupId: aws.String("sg-other")})
	}, func(f *fakeEgress) {
		f.interfaces = append(f.interfaces, types.NetworkInterface{NetworkInterfaceId: aws.String("eni-other")})
	}, func(f *fakeEgress) { f.group.Tags = nil }, func(f *fakeEgress) { f.group.VpcId = aws.String("vpc-other") }} {
		e, f, p := egressFixture()
		mutate(f)
		if e.ApplyEgress(context.Background(), p) == nil || len(f.events) > 0 {
			t.Fatal("unsafe mutation")
		}
	}
}
