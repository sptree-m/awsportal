package awsapi

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/sptree-m/awsportal/internal/store"
	"net/netip"
	"sort"
	"strings"
)

type EgressController interface {
	InspectEgress(context.Context, store.EgressPolicy) (string, error)
	ApplyEgress(context.Context, store.EgressPolicy) error
}
type egressAPI interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeNetworkInterfaces(context.Context, *ec2.DescribeNetworkInterfacesInput, ...func(*ec2.Options)) (*ec2.DescribeNetworkInterfacesOutput, error)
	DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	RevokeSecurityGroupEgress(context.Context, *ec2.RevokeSecurityGroupEgressInput, ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error)
	AuthorizeSecurityGroupEgress(context.Context, *ec2.AuthorizeSecurityGroupEgressInput, ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error)
}
type Egress struct{ client egressAPI }

func NewEgress(cfg aws.Config) *Egress { return &Egress{ec2.NewFromConfig(cfg)} }
func (e *Egress) checked(ctx context.Context, p store.EgressPolicy) (types.SecurityGroup, error) {
	fail := func(msg string) (types.SecurityGroup, error) { return types.SecurityGroup{}, fmt.Errorf("%s", msg) }
	if p.SecurityGroupID == p.ProxyGroupID || p.SecurityGroupID == "" || p.ProxyGroupID == "" {
		return fail("専用SGとプロキシSGを別々に指定してください")
	}
	xs, err := e.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{p.InstanceID}})
	if err != nil {
		return types.SecurityGroup{}, err
	}
	var instance *types.Instance
	for _, r := range xs.Reservations {
		for _, i := range r.Instances {
			if aws.ToString(i.InstanceId) == p.InstanceID {
				copy := i
				instance = &copy
			}
		}
	}
	if instance == nil || len(instance.NetworkInterfaces) != 1 {
		return fail("ネットワークインターフェースが1個のEC2だけ対応しています")
	}
	ni := instance.NetworkInterfaces[0]
	if len(ni.Groups) != 1 || aws.ToString(ni.Groups[0].GroupId) != p.SecurityGroupID {
		return fail("EC2に専用SGだけを割り当ててください。他のSGは迂回経路になります")
	}
	nis, err := e.client.DescribeNetworkInterfaces(ctx, &ec2.DescribeNetworkInterfacesInput{Filters: []types.Filter{{Name: aws.String("group-id"), Values: []string{p.SecurityGroupID}}}})
	if err != nil {
		return types.SecurityGroup{}, err
	}
	if len(nis.NetworkInterfaces) != 1 || aws.ToString(nis.NetworkInterfaces[0].NetworkInterfaceId) != aws.ToString(ni.NetworkInterfaceId) {
		return fail("SGが他のENIと共有されています")
	}
	gs, err := e.client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{p.SecurityGroupID, p.ProxyGroupID}})
	if err != nil {
		return types.SecurityGroup{}, err
	}
	var sg, proxy *types.SecurityGroup
	for _, g := range gs.SecurityGroups {
		copy := g
		if aws.ToString(g.GroupId) == p.SecurityGroupID {
			sg = &copy
		}
		if aws.ToString(g.GroupId) == p.ProxyGroupID {
			proxy = &copy
		}
	}
	if sg == nil || proxy == nil || aws.ToString(sg.VpcId) != aws.ToString(proxy.VpcId) || aws.ToString(sg.VpcId) != aws.ToString(instance.VpcId) {
		return fail("専用SGとプロキシSGはEC2と同じVPCが必要です")
	}
	managed := false
	for _, t := range sg.Tags {
		if aws.ToString(t.Key) == "awsportal:egress-instance" && aws.ToString(t.Value) == p.InstanceID {
			managed = true
		}
	}
	if !managed {
		return fail("SGにawsportal:egress-instanceタグが必要です")
	}
	return *sg, nil
}
func desiredPermissions(p store.EgressPolicy) ([]types.IpPermission, error) {
	if p.ProxyPort < 1 || p.ProxyPort > 65535 || len(p.Rules) > 50 {
		return nil, fmt.Errorf("ポート/ルール数が不正です")
	}
	out := []types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(p.ProxyPort), ToPort: aws.Int32(p.ProxyPort), UserIdGroupPairs: []types.UserIdGroupPair{{GroupId: aws.String(p.ProxyGroupID)}}}}
	seen := map[string]bool{}
	for _, r := range p.Rules {
		cidr, err := store.NormalizeCIDR(r.CIDR)
		if err != nil || r.Protocol != "tcp" && r.Protocol != "udp" || r.From < 1 || r.To < r.From || r.To > 65535 {
			return nil, fmt.Errorf("IPルールが不正です")
		}
		key := fmt.Sprint(cidr, r.Protocol, r.From, r.To)
		if seen[key] {
			continue
		}
		seen[key] = true
		x := types.IpPermission{IpProtocol: aws.String(r.Protocol), FromPort: aws.Int32(r.From), ToPort: aws.Int32(r.To)}
		prefix, _ := netip.ParsePrefix(cidr)
		if prefix.Addr().Is4() {
			x.IpRanges = []types.IpRange{{CidrIp: aws.String(cidr)}}
		} else {
			x.Ipv6Ranges = []types.Ipv6Range{{CidrIpv6: aws.String(cidr)}}
		}
		out = append(out, x)
	}
	return out, nil
}
func permissionKeys(xs []types.IpPermission) string {
	keys := []string{}
	for _, x := range xs {
		proto := aws.ToString(x.IpProtocol)
		if proto == "6" {
			proto = "tcp"
		}
		if proto == "17" {
			proto = "udp"
		}
		base := fmt.Sprintf("%s:%d:%d:", proto, aws.ToInt32(x.FromPort), aws.ToInt32(x.ToPort))
		for _, r := range x.IpRanges {
			keys = append(keys, base+aws.ToString(r.CidrIp))
		}
		for _, r := range x.Ipv6Ranges {
			keys = append(keys, base+aws.ToString(r.CidrIpv6))
		}
		for _, r := range x.UserIdGroupPairs {
			keys = append(keys, base+"sg:"+aws.ToString(r.GroupId))
		}
		for _, r := range x.PrefixListIds {
			keys = append(keys, base+"pl:"+aws.ToString(r.PrefixListId))
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
}
func (e *Egress) InspectEgress(ctx context.Context, p store.EgressPolicy) (string, error) {
	want, err := desiredPermissions(p)
	if err != nil {
		return "", err
	}
	sg, err := e.checked(ctx, p)
	if err != nil {
		return "", err
	}
	if permissionKeys(want) == permissionKeys(sg.IpPermissionsEgress) {
		return "AWS設定一致", nil
	}
	return "AWS設定不一致（未適用または外部変更）", nil
}
func (e *Egress) ApplyEgress(ctx context.Context, p store.EgressPolicy) error {
	want, err := desiredPermissions(p)
	if err != nil {
		return err
	}
	sg, err := e.checked(ctx, p)
	if err != nil {
		return err
	}
	if permissionKeys(want) == permissionKeys(sg.IpPermissionsEgress) {
		return nil
	}
	// Revoke first: partial failures must not reopen broad access. Ingress is untouched.
	if len(sg.IpPermissionsEgress) > 0 {
		_, err = e.client.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: aws.String(p.SecurityGroupID), IpPermissions: sg.IpPermissionsEgress})
		if err != nil {
			return err
		}
	}

	checked, verifyErr := e.checked(ctx, p)
	if verifyErr != nil {
		return verifyErr
	}
	if len(checked.IpPermissionsEgress) != 0 {
		return fmt.Errorf("旧outboundの削除を確認できません。AWS状態を確認してください")
	}
	_, err = e.client.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{GroupId: aws.String(p.SecurityGroupID), IpPermissions: want})
	if err != nil {
		return fmt.Errorf("旧許可削除後、新許可の追加に失敗。AWSの実状態を確認して再適用してください: %w", err)
	}
	status, err := e.InspectEgress(ctx, p)
	if err != nil {
		return err
	}
	if status != "AWS設定一致" {
		return fmt.Errorf("AWS反映未確認。状態確認または再適用してください")
	}
	return nil
}
