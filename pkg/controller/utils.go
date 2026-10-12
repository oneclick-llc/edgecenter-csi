package controller

import (
	"fmt"
	"time"

	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/sets"
)

func volumeType(vtype string) edgecloudV2.VolumeType {
	switch edgecloudV2.VolumeType(vtype) {
	case edgecloudV2.VolumeTypeStandard:
		return edgecloudV2.VolumeTypeStandard
	case edgecloudV2.VolumeTypeSsdHiIops:
		return edgecloudV2.VolumeTypeSsdHiIops
	case edgecloudV2.VolumeTypeCold:
		return edgecloudV2.VolumeTypeCold
	case edgecloudV2.VolumeTypeUltra:
		return edgecloudV2.VolumeTypeUltra
	default:
		return edgecloudV2.VolumeTypeStandard
	}
}

func volumeMetadata(clusterID string, req *csi.CreateVolumeRequest) edgecloudV2.Metadata {
	md := edgecloudV2.Metadata{
		"csi.edgecenter.org/cluster": clusterID,
	}
	for _, mKey := range []string{"csi.storage.k8s.io/pvc/name", "csi.storage.k8s.io/sourcevolumeid", "csi.storage.k8s.io/pvc/namespace", "csi.storage.k8s.io/pv/name"} {
		if v, ok := req.Parameters[mKey]; ok {
			md[mKey] = v
		}
	}
	return md
}

func prepareCreateResponse(vol *edgecloudV2.Volume, ignoreVolumeAZ bool, topologyRegion string, accessibleTopologyReq *csi.TopologyRequirement) *csi.CreateVolumeResponse {
	var volContentSource *csi.VolumeContentSource
	if len(vol.SnapshotIDs) > 0 {
		volContentSource = &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{
				Snapshot: &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: vol.SnapshotIDs[0],
				},
			},
		}
	}

	// If ignore-volume-az is true, don't set the accessible topology to volume az,
	// use from preferred topologies instead.
	var accessibleTopology []*csi.Topology
	if topologyRegion != "" {
		accessibleTopology = []*csi.Topology{{
			Segments: map[string]string{"topology.kubernetes.io/region": topologyRegion},
		}}
	} else if ignoreVolumeAZ {
		if accessibleTopologyReq != nil {
			accessibleTopology = accessibleTopologyReq.GetPreferred()
		}
	} else {
		// TODO implement
		accessibleTopology = []*csi.Topology{
			{
				//Segments: map[string]string{topologyKey: vol.AvailabilityZone},
			},
		}
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:           vol.ID,
			CapacityBytes:      int64(vol.Size * 1024 * 1024 * 1024),
			AccessibleTopology: accessibleTopology,
			ContentSource:      volContentSource,
		},
	}
}

func newCapability(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}

// validateCapabilities validates the requested capabilities. It returns a list
// of violations which may be empty if no violations were found.
func validateCapabilities(caps []*csi.VolumeCapability) []string {
	violations := sets.NewString()
	for _, c := range caps {
		if c.GetAccessMode().GetMode() != supportedAccessMode.GetMode() {
			violations.Insert(fmt.Sprintf("unsupported access mode %s", c.GetAccessMode().GetMode().String()))
		}
		accessType := c.GetAccessType()
		switch accessType.(type) {
		case *csi.VolumeCapability_Block:
		case *csi.VolumeCapability_Mount:
		default:
			violations.Insert("unsupported access type")
		}
	}

	return violations.List()
}

// RoundUpSize calculates how many allocation units are needed to accommodate
// a volume of given size. E.g. when user wants 1500MiB volume, while AWS EBS
// allocates volumes in gibibyte-sized chunks,
// RoundUpSize(1500 * 1024*1024, 1024*1024*1024) returns '2'
// (2 GiB is the smallest allocatable volume that can hold 1500MiB)
func roundUpSize(volumeSizeBytes int64, allocationUnitBytes int64) int64 {
	roundedUp := volumeSizeBytes / allocationUnitBytes
	if volumeSizeBytes%allocationUnitBytes > 0 {
		roundedUp++
	}
	return roundedUp
}

func parseTimeIsoString(timeString string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15:04:05-0700", timeString)
	if err != nil {
		return time.Time{}, status.Errorf(codes.Internal, "Failed to parse time string from snapshot error %v", err)
	}
	return t, nil
}

func snapshotMetadata(clusterID string, req *csi.CreateSnapshotRequest) edgecloudV2.Metadata {
	md := edgecloudV2.Metadata{
		"csi.edgecenter.org/cluster": clusterID,
	}
	for _, mKey := range []string{"csi.storage.k8s.io/volumesnapshot/name", "csi.storage.k8s.io/sourcevolumeid", "csi.storage.k8s.io/volumesnapshot/namespace", "csi.storage.k8s.io/volumesnapshotcontent/name"} {
		if v, ok := req.Parameters[mKey]; ok {
			md[mKey] = v
		}
	}
	return md
}
