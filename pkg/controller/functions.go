package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/Edge-Center/edgecentercloud-go/v2/util"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const deleteVolumeTimeout = 25 * time.Second

func (s *Service) validateTopology(req *csi.CreateVolumeRequest) error {
	if s.topologyRegion == "" || req.GetAccessibilityRequirements() == nil {
		return nil
	}
	requisite := req.GetAccessibilityRequirements().GetRequisite()
	if len(requisite) == 0 {
		return nil
	}
	for _, topology := range requisite {
		if topology.GetSegments()["topology.kubernetes.io/region"] == s.topologyRegion {
			return nil
		}
	}
	return status.Errorf(codes.InvalidArgument, "storage class topology does not include driver region %q", s.topologyRegion)
}

// CreateVolume creates a new volume from the given request. The function is idempotent.
func (s *Service) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	methodName := "CreateVolume"
	if err := s.validateTopology(req); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: Name must be provided", methodName)
	}
	if req.VolumeCapabilities == nil || len(req.VolumeCapabilities) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeCapabilities must be provided", methodName)
	}
	if violations := validateCapabilities(req.VolumeCapabilities); len(violations) > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeCapabilities cannot be satisfied: %s", methodName, strings.Join(violations, "; "))
	}

	// Volume Size - Default is 1 GiB
	sizeBytes := defaultVolumeSizeInBytes
	if req.GetCapacityRange() != nil {
		sizeBytes = req.GetCapacityRange().GetRequiredBytes()
	}
	sizeGB := int(roundUpSize(sizeBytes, giB))

	log := s.log.WithFields(logrus.Fields{
		"volume_name":         req.Name,
		"volume_size":         sizeGB,
		"method":              methodName,
		"volume_capabilities": req.VolumeCapabilities,
	})
	log.Infof("%s is called", methodName)

	// Verify a volume with the provided name doesn't already exist for this tenant
	volumes, err := util.VolumesListByName(ctx, s.cloud, req.Name)
	if err != nil && !errors.Is(err, util.ErrVolumesNotFound) {
		return nil, status.Errorf(codes.Internal, "%s: failed to get volumes by name: %s", methodName, err)
	}

	if len(volumes) == 1 {
		if sizeGB != volumes[0].Size {
			return nil, status.Errorf(codes.AlreadyExists, "%s: volume already exists with same name and different capacity", methodName)
		}
		return prepareCreateResponse(&volumes[0], true, s.topologyRegion, req.GetAccessibilityRequirements()), nil
	} else if len(volumes) > 1 {
		return nil, status.Errorf(codes.Internal, "%s: multiple volumes reported with same name", methodName)
	}

	content := req.GetVolumeContentSource()
	var sourceVolID string
	if content != nil && content.GetVolume() != nil {
		sourceVolID = content.GetVolume().GetVolumeId()
		exist, err := util.ResourceIsExist(ctx, s.cloud.Volumes.Get, sourceVolID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to retrieve the source volume %s: %v", methodName, sourceVolID, err)
		}
		if !exist {
			return nil, status.Errorf(codes.NotFound, "%s: source Volume %s not found", methodName, sourceVolID)
		}
	}

	var snapshotID string
	if content != nil && content.GetSnapshot() != nil {
		snapshotID = content.GetSnapshot().GetSnapshotId()
		exist, err := util.ResourceIsExist(ctx, s.cloud.Snapshots.Get, snapshotID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to retrieve the snapshot %s: %v", methodName, snapshotID, err)
		}
		if !exist {
			return nil, status.Errorf(codes.NotFound, "%s: volumeContentSource Snapshot %s not found", methodName, snapshotID)
		}
	}

	md := volumeMetadata(s.clusterID, req)
	var volume *edgecloudV2.Volume

	if snapshotID != "" {
		s.log.WithFields(logrus.Fields{"snapshot_id": snapshotID}).Info("using snapshot as volume source")
		volume, err = s.ensureVolumeFromSnapshot(ctx, req.Name, snapshotID, req.GetParameters()["type"], sizeGB, md)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to ensure volume from snapshot: %s", methodName, err)
		}
	} else {
		volume, err = s.ensureVolumeFromNew(ctx, req.Name, req.GetParameters()["type"], sizeGB, md)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to ensure volume: %s", methodName, err)
		}
	}
	s.log.Info("volume is created")

	s.log.Infof("%s SnapshotIDs", volume.SnapshotIDs)

	return prepareCreateResponse(volume, true, s.topologyRegion, req.AccessibilityRequirements), nil
}

// DeleteVolume deletes the given volume. The function is idempotent.
func (s *Service) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	const methodName = "DeleteVolume"

	volumeID := req.GetVolumeId()
	if volumeID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id": volumeID,
		"method":    methodName,
	})
	log.Infof("%s is called", methodName)

	_, resp, err := s.cloud.Volumes.Get(ctx, volumeID)

	if err != nil {
		if util.IsNotFoundErr(resp) {
			log.Info("volume does not exist, skipping deletion")
			return &csi.DeleteVolumeResponse{}, nil
		}

		return nil, status.Errorf(codes.Internal, "%s: failed to get volume %q: %v", methodName, volumeID, err)
	}

	snapshots, _, err := s.cloud.Snapshots.List(ctx, &edgecloudV2.SnapshotListOptions{VolumeID: volumeID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed to list snapshots for volume %q: %v", methodName,
			volumeID,
			err,
		)
	}

	if len(snapshots) > 0 {
		snapshotIDs := make([]string, 0, len(snapshots))
		for _, snapshot := range snapshots {
			snapshotIDs = append(snapshotIDs, snapshot.ID)
		}

		log.WithField("snapshot_ids", snapshotIDs).
			Warn("volume deletion is blocked by existing snapshots")

		return nil, status.Errorf(codes.FailedPrecondition,
			"%s: volume %q cannot be deleted because it has existing snapshots %v; "+
				"delete the snapshots first",
			methodName,
			volumeID,
			snapshotIDs,
		)
	}

	if err := util.DeleteResourceIfExist(ctx, s.cloud, s.cloud.Volumes, volumeID, deleteVolumeTimeout); err != nil {
		return nil, status.Errorf(codes.Internal, "%s: volume %q was not deleted: %v", methodName, volumeID,
			err,
		)
	}

	log.Info("volume is deleted")
	return &csi.DeleteVolumeResponse{}, nil
}

// ListVolumes returns a list of all requested volumes
func (s *Service) ListVolumes(ctx context.Context, _ *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	methodName := "ListVolumes"
	volumes, _, err := s.cloud.Volumes.List(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: get volume list failed with error %v", methodName, err)
	}

	log := s.log.WithFields(logrus.Fields{"method": methodName})
	log.Infof("%s is called", methodName)

	entries := make([]*csi.ListVolumesResponse_Entry, 0, len(volumes))
	for _, v := range volumes {
		volumeStatus := &csi.ListVolumesResponse_VolumeStatus{}
		volumeStatus.PublishedNodeIds = make([]string, 0, len(v.Attachments))
		for _, attachment := range v.Attachments {
			volumeStatus.PublishedNodeIds = append(volumeStatus.PublishedNodeIds, attachment.ServerID)
		}
		entry := csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      v.ID,
				CapacityBytes: int64(v.Size * giB),
			},
			Status: volumeStatus,
		}
		entries = append(entries, &entry)
	}
	log.WithField("num_volume_entries", len(entries)).Info("volumes listed")
	return &csi.ListVolumesResponse{Entries: entries}, nil
}

// CreateSnapshot will be called by the CO to create a new snapshot from a source volume on behalf of a user.
func (s *Service) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	methodName := "CreateSnapshot"
	if req.GetName() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: Name must be provided", methodName)
	}

	if req.GetSourceVolumeId() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: SourceVolumeID must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"req_name":             req.GetName(),
		"req_source_volume_id": req.GetSourceVolumeId(),
		"req_parameters":       req.GetParameters(),
		"method":               methodName,
	})

	log.Infof("%s is called", methodName)
	log.Infof("%s volumesource", req.SourceVolumeId)

	// Verify a snapshot with the provided name doesn't already exist for this tenant
	snaps, err := util.SnapshotsListByNameAndVolumeID(ctx, s.cloud, req.Name, req.SourceVolumeId)

	if err != nil && !errors.Is(err, util.ErrSnapshotsNotFound) {
		return nil, status.Errorf(codes.Internal, "%s: failed to get snapshots: %s", methodName, err.Error())
	}

	var snap *edgecloudV2.Snapshot
	if len(snaps) == 1 {
		snap = &snaps[0]
		if snap.VolumeID != req.SourceVolumeId {
			return nil, status.Errorf(codes.AlreadyExists, "%s: snapshot with given name already exists, with different sourceVolumeID", methodName)
		}
		log.Info("found existing snapshot")
	} else if len(snaps) > 1 {
		log.Info("found multiple existing snapshots with selected name", req.Name)
		return nil, status.Errorf(codes.Internal, "%s multiple snapshots reported with same name", methodName)
	}

	md := snapshotMetadata(s.clusterID, req)

	snap, err = s.ensureSnapshot(ctx, req.SourceVolumeId, req.Name, md)

	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed to ensure snapshot volume: %s", methodName, err)
	}

	snapTime, err := parseTimeIsoString(snap.CreatedAt)
	if err != nil {
		return nil, err
	}

	ctime := timestamppb.New(snapTime)
	if err = ctime.CheckValid(); err != nil {
		log.Errorf("Error to convert time to timestamp: %v", err)
	}

	if err = util.WaitSnapshotStatusReady(ctx, s.cloud, snap.ID, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "%s failed with error %v", methodName, err)
	}
	log.Info("snapshot is created")
	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     snap.ID,
			SizeBytes:      int64(snap.Size * 1024 * 1024 * 1024),
			SourceVolumeId: snap.VolumeID,
			CreationTime:   ctime,
			ReadyToUse:     true,
		},
	}, nil
}

// DeleteSnapshot will be called by the CO to delete a snapshot.
func (s *Service) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	log := s.log.WithFields(logrus.Fields{
		"req_snapshot_id": req.GetSnapshotId(),
		"method":          "DeleteSnapshot",
	})

	log.Info("DeleteSnapshot is called")

	if req.SnapshotId == "" {
		return nil, status.Error(codes.InvalidArgument, "DeleteSnapshot: SnapshotID must be provided")
	}
	if err := util.DeleteResourceIfExist(ctx, s.cloud, s.cloud.Snapshots, req.SnapshotId); err != nil {
		return nil, status.Errorf(codes.Internal, "DeleteSnapshot: snapshots was not deleted with error %v", err)
	}

	log.Info("snapshot is deleted")
	return &csi.DeleteSnapshotResponse{}, nil
}

// ListSnapshots returns the information about all snapshots on the storage system within the given parameters regardless of how they were created.
// ListSnapshots should not list a snapshot that is being created but has not been cut successfully yet.
func (s *Service) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	methodName := "ListSnapshots"
	log := s.log.WithFields(logrus.Fields{
		"snapshot_id":        req.SnapshotId,
		"source_volume_id":   req.SourceVolumeId,
		"max_entries":        req.MaxEntries,
		"req_starting_token": req.StartingToken,
		"method":             methodName,
	})
	log.Infof("%s is called", methodName)

	snapshotID := req.GetSnapshotId()
	if len(snapshotID) != 0 {
		snapshot, resp, err := s.cloud.Snapshots.Get(ctx, snapshotID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "%s: failed to get snapshot %s : %v", methodName, snapshotID, err)
		}
		if resp.StatusCode == http.StatusNotFound {
			return &csi.ListSnapshotsResponse{}, nil
		}

		snapTime, err := parseTimeIsoString(snapshot.CreatedAt)
		if err != nil {
			return nil, err
		}

		ctime := timestamppb.New(snapTime)
		entry := &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SizeBytes:      int64(snapshot.Size * 1024 * 1024 * 1024),
				SnapshotId:     snapshot.ID,
				SourceVolumeId: snapshot.VolumeID,
				CreationTime:   ctime,
				ReadyToUse:     true,
			},
		}
		return &csi.ListSnapshotsResponse{Entries: []*csi.ListSnapshotsResponse_Entry{entry}}, ctime.CheckValid()
	}

	snaps, err := util.SnapshotsListByStatusAndVolumeID(ctx, s.cloud, "available", req.GetSourceVolumeId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: failed with error %v", methodName, err)
	}

	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, len(snaps))
	for _, v := range snaps {

		snapTime, err := parseTimeIsoString(v.CreatedAt)
		if err != nil {
			return nil, err
		}

		ctime := timestamppb.New(snapTime)
		if err = ctime.CheckValid(); err != nil {
			log.Errorf("error to convert time to timestamp: %v", err)
		}
		entry := csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SizeBytes:      int64(v.Size * 1024 * 1024 * 1024),
				SnapshotId:     v.ID,
				SourceVolumeId: v.VolumeID,
				CreationTime:   ctime,
				ReadyToUse:     true,
			},
		}
		entries = append(entries, &entry)
	}

	log.Info("snapshots listed")
	return &csi.ListSnapshotsResponse{Entries: entries}, nil
}

// ControllerPublishVolume attaches the given volume to the node
func (s *Service) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	methodName := "ControllerPublishVolume"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}
	if req.NodeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: NodeID must be provided", methodName)
	}
	if req.VolumeCapability == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeCapability must be provided", methodName)
	}
	if req.Readonly {
		// TODO(arslan): we should return codes.InvalidArgument, but the CSI
		// test fails, because according to the CSI Spec, this flag cannot be
		// changed on the same volume. However we don't use this flag at all,
		// as there are no `readonly` attachable volumes.
		return nil, status.Errorf(codes.AlreadyExists, "%s: read only Volumes are not supported", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id": req.VolumeId,
		"node_id":   req.NodeId,
		"method":    methodName,
	})
	log.Infof("%s is called", methodName)

	devicePath, err := s.ensureAttachmentVolume(ctx, req.VolumeId, req.NodeId)
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, status.Errorf(codes.Internal, "%s: cannot attach volume: %s", methodName, err)
	}
	log.Info("volume is attached")
	return &csi.ControllerPublishVolumeResponse{PublishContext: map[string]string{"DevicePath": devicePath}}, nil
}

// ControllerUnpublishVolume deattaches the given volume from the node
func (s *Service) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	methodName := "ControllerUnpublishVolume"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}
	if req.NodeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: NodeID must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id": req.VolumeId,
		"node_id":   req.NodeId,
		"method":    methodName,
	})
	log.Infof("%s is called", methodName)
	if err := s.ensureDetachmentVolume(ctx, req.VolumeId, req.NodeId); err != nil {
		return nil, status.Errorf(codes.Internal, "%s: cannot detach volume: %s", methodName, err.Error())
	}
	log.Info("volume was detached")
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// GetCapacity returns the capacity of the storage pool
func (s *Service) GetCapacity(_ context.Context, _ *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetCapacity is not yet implemented")
}

// ValidateVolumeCapabilities checks whether the volume capabilities requested are supported.
func (s *Service) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	methodName := "ValidateVolumeCapabilities"
	if req.VolumeId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID must be provided", methodName)
	}

	if req.VolumeCapabilities == nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeCapabilities must be provided", methodName)
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id":              req.VolumeId,
		"volume_capabilities":    req.VolumeCapabilities,
		"supported_capabilities": supportedAccessMode,
		"method":                 methodName,
	})
	log.Infof("%s is capabilities called", methodName)

	exist, err := util.ResourceIsExist(ctx, s.cloud.Volumes.Get, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: get volume failed with error %s", methodName, err.Error())
	}
	if !exist {
		return nil, status.Errorf(codes.NotFound, "%s: volume %s not found", methodName, req.VolumeId)
	}
	resp := &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: supportedAccessMode,
				},
			},
		},
	}
	log.WithField("confirmed", resp.Confirmed).Info("supported capabilities")
	return resp, nil
}

// ControllerGetCapabilities returns the capabilities of the controller service.
func (s *Service) ControllerGetCapabilities(_ context.Context, _ *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	var caps []*csi.ControllerServiceCapability
	for _, cp := range Capabilities {
		caps = append(caps, newCapability(cp))
	}
	resp := &csi.ControllerGetCapabilitiesResponse{
		Capabilities: caps,
	}
	s.log.WithFields(logrus.Fields{"response": resp, "method": "ControllerGetCapabilities"}).Info("ControllerGetCapabilities is called")
	return resp, nil
}

// ControllerGetVolume gets a specific volume.
func (s *Service) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	methodName := "ControllerGetVolume"
	volumeID := req.GetVolumeId()
	if volumeID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s: VolumeID not provided", methodName)
	}

	volume, resp, err := s.cloud.Volumes.Get(ctx, volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s: get volume failed with error %v", methodName, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, status.Errorf(codes.NotFound, "%s: volume not found", methodName)
	}

	entry := csi.ControllerGetVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: int64(volume.Size * giB),
		},
	}

	volumeStatus := &csi.ControllerGetVolumeResponse_VolumeStatus{}
	volumeStatus.PublishedNodeIds = make([]string, 0, len(volume.Attachments))
	for _, attachment := range volume.Attachments {
		volumeStatus.PublishedNodeIds = append(volumeStatus.PublishedNodeIds, attachment.ServerID)
	}
	entry.Status = volumeStatus
	return &entry, nil
}

// ControllerExpandVolume is called from the resizer to increase the volume size.
func (s *Service) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "VolumeID not provided")
	}

	capacityRange := req.GetCapacityRange()
	if capacityRange == nil {
		return nil, status.Error(codes.InvalidArgument, "Capacity range not provided")
	}

	log := s.log.WithFields(logrus.Fields{
		"volume_id": req.VolumeId,
		"method":    "controller_expand_volume",
	})

	log.Info("controller expand volume called")

	volSizeBytes := req.GetCapacityRange().GetRequiredBytes()
	size := int(roundUpSize(volSizeBytes, giB))
	maxVolSize := capacityRange.GetLimitBytes()

	if maxVolSize > 0 && maxVolSize < int64(size*giB) {
		return nil, status.Error(codes.OutOfRange, "after round-up, volume size exceeds the limit specified")
	}

	if err := s.ensureExpandingVolume(ctx, volumeID, size); err != nil {
		return nil, err
	}
	log.Info("volume was resized")
	nodeExpansionRequired := true
	if req.GetVolumeCapability() != nil {
		if _, ok := req.GetVolumeCapability().GetAccessType().(*csi.VolumeCapability_Block); ok {
			log.Info("node expansion is not required for block volumes")
			nodeExpansionRequired = false
		}
	}

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         volSizeBytes,
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

// ControllerModifyVolume TODO need to implement.
func (s *Service) ControllerModifyVolume(_ context.Context, _ *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume unsupported")
}
