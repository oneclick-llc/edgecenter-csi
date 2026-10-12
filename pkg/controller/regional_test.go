package controller

import (
	"context"
	"testing"

	"ec-csi-plugin/pkg/volumeid"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestRegionalController(regions []Region, legacyRegionID int64) *regionalController {
	return NewMulti(regions, "cluster-test", legacyRegionID, logrus.NewEntry(logrus.New())).(*regionalController)
}

func topology(region string) *csi.Topology {
	return &csi.Topology{Segments: map[string]string{TopologyKey: region}}
}

func TestRegionForCreate(t *testing.T) {
	regions := []Region{
		{ID: 10, Topology: "moscow"},
		{ID: 112, Topology: "moscow-2"},
	}
	controller := newTestRegionalController(regions, 10)

	tests := []struct {
		name    string
		request *csi.CreateVolumeRequest
		wantID  int64
		wantErr bool
	}{
		{
			name:    "storage class region parameter",
			request: &csi.CreateVolumeRequest{Parameters: map[string]string{RegionParameter: "112"}},
			wantID:  112,
		},
		{
			name: "single requisite topology",
			request: &csi.CreateVolumeRequest{AccessibilityRequirements: &csi.TopologyRequirement{
				Requisite: []*csi.Topology{topology("moscow")},
			}},
			wantID: 10,
		},
		{
			name: "preferred topology within requisite",
			request: &csi.CreateVolumeRequest{AccessibilityRequirements: &csi.TopologyRequirement{
				Requisite: []*csi.Topology{topology("moscow"), topology("moscow-2")},
				Preferred: []*csi.Topology{topology("moscow-2"), topology("moscow")},
			}},
			wantID: 112,
		},
		{
			name: "unknown requisite alongside supported region",
			request: &csi.CreateVolumeRequest{AccessibilityRequirements: &csi.TopologyRequirement{
				Requisite: []*csi.Topology{topology("other-region"), topology("moscow")},
			}},
			wantID: 10,
		},
		{
			name: "reject region outside requisite",
			request: &csi.CreateVolumeRequest{
				Parameters: map[string]string{RegionParameter: "112"},
				AccessibilityRequirements: &csi.TopologyRequirement{
					Requisite: []*csi.Topology{topology("moscow")},
				},
			},
			wantErr: true,
		},
		{
			name:    "reject zero region parameter",
			request: &csi.CreateVolumeRequest{Parameters: map[string]string{RegionParameter: "0"}},
			wantErr: true,
		},
		{
			name:    "multi-region request without region",
			request: &csi.CreateVolumeRequest{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, regionID, err := controller.regionForCreate(tt.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("regionForCreate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if regionID != tt.wantID || region != controller.byID[tt.wantID] {
				t.Fatalf("regionForCreate() = (%d, %p), want (%d, %p)", regionID, region, tt.wantID, controller.byID[tt.wantID])
			}
		})
	}
}

func TestRegionForCreateUsesSoleRegionWithoutTopology(t *testing.T) {
	controller := newTestRegionalController([]Region{{ID: 10}}, 10)
	region, regionID, err := controller.regionForCreate(&csi.CreateVolumeRequest{})
	if err != nil {
		t.Fatalf("regionForCreate() error = %v", err)
	}
	if regionID != 10 || region != controller.byID[10] {
		t.Fatalf("regionForCreate() = (%d, %p), want (10, %p)", regionID, region, controller.byID[10])
	}
}

func TestRegionForHandleSupportsLegacyAndRegionalHandles(t *testing.T) {
	controller := newTestRegionalController([]Region{
		{ID: 10, Topology: "moscow"},
		{ID: 112, Topology: "moscow-2"},
	}, 10)

	tests := []struct {
		name       string
		handle     string
		wantRegion int64
		wantID     string
		wantCode   codes.Code
		wantError  bool
	}{
		{name: "regional handle", handle: volumeid.Encode(112, "volume-112"), wantRegion: 112, wantID: "volume-112"},
		{name: "legacy handle uses configured region", handle: "legacy-volume", wantRegion: 10, wantID: "legacy-volume"},
		{name: "unknown regional handle", handle: volumeid.Encode(999, "volume-999"), wantError: true, wantCode: codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			region, providerID, regionID, err := controller.regionForHandle(tt.handle)
			if tt.wantError {
				if status.Code(err) != tt.wantCode {
					t.Fatalf("regionForHandle() error = %v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("regionForHandle() error = %v", err)
			}
			if regionID != tt.wantRegion || providerID != tt.wantID || region != controller.byID[tt.wantRegion] {
				t.Fatalf("regionForHandle() = (%d, %q, %p), want (%d, %q, %p)", regionID, providerID, region, tt.wantRegion, tt.wantID, controller.byID[tt.wantRegion])
			}
		})
	}

	withoutLegacyRegion := newTestRegionalController([]Region{
		{ID: 10, Topology: "moscow"},
		{ID: 112, Topology: "moscow-2"},
	}, 0)
	if _, _, _, err := withoutLegacyRegion.regionForHandle("legacy-volume"); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("regionForHandle() error = %v, want InvalidArgument for an unmapped legacy handle", err)
	}
}

func TestSingleRegionRoutesLegacyHandlesWithoutExplicitFallback(t *testing.T) {
	controller := newTestRegionalController([]Region{{ID: 10, Topology: "moscow"}}, 0)
	region, providerID, regionID, err := controller.regionForHandle("legacy-volume")
	if err != nil {
		t.Fatalf("regionForHandle() error = %v", err)
	}
	if region != controller.byID[10] || regionID != 10 || providerID != "legacy-volume" {
		t.Fatalf("regionForHandle() = (%d, %q, %p)", regionID, providerID, region)
	}
}

func TestCreateVolumeRejectsCrossRegionSource(t *testing.T) {
	controller := newTestRegionalController([]Region{
		{ID: 10, Topology: "moscow"},
		{ID: 112, Topology: "moscow-2"},
	}, 10)
	request := &csi.CreateVolumeRequest{
		Parameters: map[string]string{RegionParameter: "112"},
		VolumeContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Volume{Volume: &csi.VolumeContentSource_VolumeSource{
				VolumeId: volumeid.Encode(10, "source-volume"),
			}},
		},
	}
	_, err := controller.CreateVolume(context.Background(), request)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateVolume() error = %v, want InvalidArgument for cross-region source", err)
	}
}

func TestEncodeVolumeAndSnapshot(t *testing.T) {
	volume := &csi.Volume{
		VolumeId: "volume-10",
		ContentSource: &csi.VolumeContentSource{
			Type: &csi.VolumeContentSource_Snapshot{Snapshot: &csi.VolumeContentSource_SnapshotSource{SnapshotId: "snapshot-10"}},
		},
	}
	encodeVolume(volume, 10)
	if volume.GetVolumeId() != volumeid.Encode(10, "volume-10") {
		t.Fatalf("volume ID = %q", volume.GetVolumeId())
	}
	if got := volume.GetContentSource().GetSnapshot().GetSnapshotId(); got != volumeid.Encode(10, "snapshot-10") {
		t.Fatalf("snapshot source ID = %q", got)
	}

	snapshot := &csi.Snapshot{SnapshotId: "snapshot-10", SourceVolumeId: "volume-10"}
	encodeSnapshot(snapshot, 10)
	if snapshot.GetSnapshotId() != volumeid.Encode(10, "snapshot-10") || snapshot.GetSourceVolumeId() != volumeid.Encode(10, "volume-10") {
		t.Fatalf("encoded snapshot = (%q, %q)", snapshot.GetSnapshotId(), snapshot.GetSourceVolumeId())
	}
}
