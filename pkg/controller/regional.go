package controller

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	edgecloudV2 "github.com/Edge-Center/edgecentercloud-go/v2"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"ec-csi-plugin/pkg/volumeid"
)

const (
	RegionParameter = "csi.edgecenter.org/region-id"
	TopologyKey     = "topology.kubernetes.io/region"
)

// Region binds one EdgeCenter API region to the topology advertised by its nodes.
type Region struct {
	ID       int64
	Topology string
	Cloud    *edgecloudV2.Client
}

type regionalController struct {
	csi.UnimplementedControllerServer
	byID           map[int64]*Service
	byTopology     map[string]int64
	regions        []Region
	legacyRegionID int64
	log            *logrus.Entry
}

// NewMulti creates one CSI controller that routes requests to region-scoped
// EdgeCenter API clients while retaining one cluster-wide CSI driver name.
func NewMulti(regions []Region, clusterID string, legacyRegionID int64, log *logrus.Entry) csi.ControllerServer {
	routed := &regionalController{
		byID:           make(map[int64]*Service, len(regions)),
		byTopology:     make(map[string]int64, len(regions)),
		regions:        append([]Region(nil), regions...),
		legacyRegionID: legacyRegionID,
		log:            log,
	}
	sort.Slice(routed.regions, func(i, j int) bool { return routed.regions[i].ID < routed.regions[j].ID })
	if routed.legacyRegionID == 0 && len(routed.regions) == 1 {
		routed.legacyRegionID = routed.regions[0].ID
	}
	for _, region := range routed.regions {
		routed.byID[region.ID] = &Service{
			cloud:          region.Cloud,
			clusterID:      clusterID,
			topologyRegion: region.Topology,
			log:            log.WithField("region_id", region.ID),
		}
		if region.Topology != "" {
			routed.byTopology[region.Topology] = region.ID
		}
	}
	return routed
}

func (s *regionalController) regionByID(regionID int64) (*Service, error) {
	region, ok := s.byID[regionID]
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported EdgeCenter region ID %d", regionID)
	}
	return region, nil
}

func (s *regionalController) regionForHandle(handle string) (*Service, string, int64, error) {
	regionID, providerID, err := volumeid.Decode(handle)
	if err != nil {
		return nil, "", 0, status.Error(codes.InvalidArgument, err.Error())
	}
	if regionID == 0 {
		regionID = s.legacyRegionID
	}
	if regionID == 0 {
		return nil, "", 0, status.Error(codes.InvalidArgument, "legacy volume handle has no region prefix; configure regionID")
	}
	region, err := s.regionByID(regionID)
	if err != nil {
		return nil, "", 0, err
	}
	return region, providerID, regionID, nil
}

func (s *regionalController) regionForCreate(req *csi.CreateVolumeRequest) (*Service, int64, error) {
	var selectedID int64
	if rawID := req.GetParameters()[RegionParameter]; rawID != "" {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			return nil, 0, status.Errorf(codes.InvalidArgument, "invalid %s parameter %q", RegionParameter, rawID)
		}
		selectedID = id
	}

	requisiteIDs := make(map[int64]struct{})
	hasRegionalRequisite := false
	if requirements := req.GetAccessibilityRequirements(); requirements != nil {
		for _, topology := range requirements.GetRequisite() {
			name := topology.GetSegments()[TopologyKey]
			if name == "" {
				continue
			}
			hasRegionalRequisite = true
			id, ok := s.byTopology[name]
			if ok {
				requisiteIDs[id] = struct{}{}
			}
		}
	}

	if selectedID == 0 && len(requisiteIDs) == 1 {
		for id := range requisiteIDs {
			selectedID = id
		}
	}
	if selectedID == 0 {
		if requirements := req.GetAccessibilityRequirements(); requirements != nil {
			for _, topology := range requirements.GetPreferred() {
				if id, ok := s.byTopology[topology.GetSegments()[TopologyKey]]; ok {
					selectedID = id
					break
				}
			}
		}
	}
	if selectedID == 0 && len(requisiteIDs) > 1 {
		for _, region := range s.regions {
			if _, ok := requisiteIDs[region.ID]; ok {
				selectedID = region.ID
				break
			}
		}
	}
	if selectedID == 0 && len(s.regions) == 1 {
		selectedID = s.regions[0].ID
	}
	if selectedID == 0 {
		return nil, 0, status.Errorf(codes.InvalidArgument, "volume request does not identify an EdgeCenter region through %s or %s", RegionParameter, TopologyKey)
	}
	if hasRegionalRequisite {
		if _, ok := requisiteIDs[selectedID]; !ok {
			return nil, 0, status.Errorf(codes.InvalidArgument, "region ID %d is not allowed by CSI accessibility requirements", selectedID)
		}
	}
	region, err := s.regionByID(selectedID)
	return region, selectedID, err
}

func clone[T proto.Message](message T) T {
	return proto.Clone(message).(T)
}

func encodeVolume(volume *csi.Volume, regionID int64) {
	if volume == nil {
		return
	}
	volume.VolumeId = volumeid.Encode(regionID, volume.VolumeId)
	if source := volume.GetContentSource(); source != nil {
		switch content := source.GetType().(type) {
		case *csi.VolumeContentSource_Volume:
			content.Volume.VolumeId = volumeid.Encode(regionID, content.Volume.VolumeId)
		case *csi.VolumeContentSource_Snapshot:
			content.Snapshot.SnapshotId = volumeid.Encode(regionID, content.Snapshot.SnapshotId)
		}
	}
}

func encodeSnapshot(snapshot *csi.Snapshot, regionID int64) {
	if snapshot == nil {
		return
	}
	snapshot.SnapshotId = volumeid.Encode(regionID, snapshot.SnapshotId)
	snapshot.SourceVolumeId = volumeid.Encode(regionID, snapshot.SourceVolumeId)
}

func (s *regionalController) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	region, regionID, err := s.regionForCreate(req)
	if err != nil {
		return nil, err
	}
	request := clone(req)
	if source := request.GetVolumeContentSource(); source != nil {
		switch content := source.GetType().(type) {
		case *csi.VolumeContentSource_Volume:
			id, providerID, _, decodeErr := s.regionForHandle(content.Volume.GetVolumeId())
			if decodeErr != nil {
				return nil, decodeErr
			}
			if id != region {
				return nil, status.Error(codes.InvalidArgument, "source volume and requested region do not match")
			}
			content.Volume.VolumeId = providerID
		case *csi.VolumeContentSource_Snapshot:
			id, providerID, _, decodeErr := s.regionForHandle(content.Snapshot.GetSnapshotId())
			if decodeErr != nil {
				return nil, decodeErr
			}
			if id != region {
				return nil, status.Error(codes.InvalidArgument, "source snapshot and requested region do not match")
			}
			content.Snapshot.SnapshotId = providerID
		}
	}
	response, err := region.CreateVolume(ctx, request)
	if err != nil {
		return nil, err
	}
	encodeVolume(response.GetVolume(), regionID)
	return response, nil
}

func (s *regionalController) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	region, providerID, _, err := s.regionForHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.VolumeId = providerID
	return region.DeleteVolume(ctx, request)
}

func (s *regionalController) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	response := &csi.ListVolumesResponse{}
	for _, region := range s.regions {
		listed, err := s.byID[region.ID].ListVolumes(ctx, clone(req))
		if err != nil {
			return nil, err
		}
		for _, entry := range listed.GetEntries() {
			wrapped := clone(entry)
			encodeVolume(wrapped.Volume, region.ID)
			response.Entries = append(response.Entries, wrapped)
		}
	}
	return response, nil
}

func (s *regionalController) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	region, providerID, regionID, err := s.regionForHandle(req.GetSourceVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.SourceVolumeId = providerID
	response, err := region.CreateSnapshot(ctx, request)
	if err != nil {
		return nil, err
	}
	encodeSnapshot(response.GetSnapshot(), regionID)
	return response, nil
}

func (s *regionalController) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	region, providerID, _, err := s.regionForHandle(req.GetSnapshotId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.SnapshotId = providerID
	return region.DeleteSnapshot(ctx, request)
}

func (s *regionalController) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	regions := s.regions
	var selected *Service
	var selectedID int64
	request := clone(req)
	if req.GetSnapshotId() != "" {
		region, providerID, selectedID, err := s.regionForHandle(req.GetSnapshotId())
		if err != nil {
			return nil, err
		}
		selected = region
		request.SnapshotId = providerID
		regions = []Region{{ID: selectedID}}
	} else if req.GetSourceVolumeId() != "" {
		region, providerID, sourceRegionID, err := s.regionForHandle(req.GetSourceVolumeId())
		if err != nil {
			return nil, err
		}
		selected = region
		selectedID = sourceRegionID
		request.SourceVolumeId = providerID
		regions = []Region{{ID: selectedID}}
	}

	response := &csi.ListSnapshotsResponse{}
	for _, region := range regions {
		service := selected
		if service == nil {
			service = s.byID[region.ID]
		}
		listed, err := service.ListSnapshots(ctx, request)
		if err != nil {
			return nil, err
		}
		for _, entry := range listed.GetEntries() {
			wrapped := clone(entry)
			encodeSnapshot(wrapped.Snapshot, region.ID)
			response.Entries = append(response.Entries, wrapped)
		}
	}
	return response, nil
}

func (s *regionalController) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	region, providerID, _, err := s.regionForHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.VolumeId = providerID
	return region.ControllerPublishVolume(ctx, request)
}

func (s *regionalController) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	region, providerID, _, err := s.regionForHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.VolumeId = providerID
	return region.ControllerUnpublishVolume(ctx, request)
}

func (s *regionalController) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	region, providerID, _, err := s.regionForHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.VolumeId = providerID
	return region.ValidateVolumeCapabilities(ctx, request)
}

func (s *regionalController) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	if len(s.regions) == 0 {
		return nil, status.Error(codes.Internal, "no EdgeCenter regions are configured")
	}
	return s.byID[s.regions[0].ID].ControllerGetCapabilities(ctx, req)
}

func (s *regionalController) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	region, providerID, regionID, err := s.regionForHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.VolumeId = providerID
	response, err := region.ControllerGetVolume(ctx, request)
	if err != nil {
		return nil, err
	}
	encodeVolume(response.GetVolume(), regionID)
	return response, nil
}

func (s *regionalController) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	region, providerID, _, err := s.regionForHandle(req.GetVolumeId())
	if err != nil {
		return nil, err
	}
	request := clone(req)
	request.VolumeId = providerID
	return region.ControllerExpandVolume(ctx, request)
}

func (s *regionalController) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	if len(s.regions) == 0 {
		return nil, status.Error(codes.Internal, "no EdgeCenter regions are configured")
	}
	return s.byID[s.regions[0].ID].GetCapacity(ctx, req)
}

func (s *regionalController) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	if len(s.regions) == 0 {
		return nil, status.Error(codes.Internal, "no EdgeCenter regions are configured")
	}
	return s.byID[s.regions[0].ID].ControllerModifyVolume(ctx, req)
}

func (s *regionalController) String() string {
	return fmt.Sprintf("EdgeCenter CSI regions: %v", s.regions)
}
