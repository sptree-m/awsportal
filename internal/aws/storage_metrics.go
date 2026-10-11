package awsapi

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"sort"
	"strings"
	"time"
)

type StorageSnapshot struct {
	ResourceID string
	Payload    string
}
type StorageMeter struct {
	efs *efs.Client
	ec2 *ec2.Client
}

func NewStorageMeter(cfg sdk.Config) *StorageMeter {
	return &StorageMeter{efs.NewFromConfig(cfg), ec2.NewFromConfig(cfg)}
}
func (m *StorageMeter) Snapshot(ctx context.Context, id string, now time.Time) (StorageSnapshot, error) {
	var value any
	if strings.HasPrefix(id, "fs-") {
		r, err := m.efs.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{FileSystemId: sdk.String(id)})
		if err != nil {
			return StorageSnapshot{}, err
		}
		if len(r.FileSystems) != 1 || r.FileSystems[0].SizeInBytes == nil {
			return StorageSnapshot{}, fmt.Errorf("EFS metered size unavailable")
		}
		fs := r.FileSystems[0]
		value = map[string]any{"record_type": "efs_metered_size", "resource_id": id, "observed_at": now.Unix(), "metered_size": fs.SizeInBytes, "throughput_mode": fs.ThroughputMode, "provisioned_mibps": fs.ProvisionedThroughputInMibps, "encrypted": fs.Encrypted}
	} else if strings.HasPrefix(id, "vol-") {
		r, err := m.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
		if err != nil {
			return StorageSnapshot{}, err
		}
		if len(r.Volumes) != 1 {
			return StorageSnapshot{}, fmt.Errorf("EBS inventory unavailable")
		}
		v := r.Volumes[0]
		value = map[string]any{"record_type": "ebs_configuration", "resource_id": id, "observed_at": now.Unix(), "size_gib": v.Size, "type": v.VolumeType, "iops": v.Iops, "throughput_mibps": v.Throughput, "state": v.State, "encrypted": v.Encrypted, "attachments": v.Attachments}
	} else {
		return StorageSnapshot{}, fmt.Errorf("unsupported storage resource")
	}
	raw, err := json.Marshal(value)
	return StorageSnapshot{id, string(raw)}, err
}

func (m *StorageMeter) Inventory(ctx context.Context, id string) ([]string, string, error) {
	r, err := m.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return nil, "", err
	}
	for _, reservation := range r.Reservations {
		for _, instance := range reservation.Instances {
			if sdk.ToString(instance.InstanceId) != id || instance.State == nil {
				continue
			}
			var ids []string
			for _, b := range instance.BlockDeviceMappings {
				if b.Ebs != nil {
					ids = append(ids, sdk.ToString(b.Ebs.VolumeId))
				}
			}
			var configurations []map[string]any
			if len(ids) > 0 {
				v, err := m.ec2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: ids})
				if err != nil {
					return nil, "", err
				}
				for _, volume := range v.Volumes {
					configurations = append(configurations, map[string]any{"id": sdk.ToString(volume.VolumeId), "size_gib": volume.Size, "type": volume.VolumeType, "iops": volume.Iops, "throughput_mibps": volume.Throughput, "encrypted": volume.Encrypted})
				}
			}
			sort.Slice(configurations, func(i, j int) bool { return configurations[i]["id"].(string) < configurations[j]["id"].(string) })
			raw, err := json.Marshal(map[string]any{"resource_id": id, "state": instance.State.Name, "storage": configurations})
			return ids, string(raw), err
		}
	}
	return nil, "", fmt.Errorf("managed instance inventory unavailable")
}
