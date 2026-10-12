package main

import "testing"

func TestValidateRegions(t *testing.T) {
	tests := []struct {
		name           string
		regions        []regionConfig
		legacyRegionID int
		role           string
		nodeTopology   string
		wantErr        bool
	}{
		{
			name:           "single legacy region without topology",
			regions:        []regionConfig{{RegionID: 10}},
			legacyRegionID: 10,
			role:           "controller",
		},
		{
			name: "multi-region controller",
			regions: []regionConfig{
				{RegionID: 10, Topology: "moscow"},
				{RegionID: 112, Topology: "moscow-2"},
			},
			legacyRegionID: 10,
			role:           "controller",
		},
		{
			name: "node region matches configuration",
			regions: []regionConfig{
				{RegionID: 10, Topology: "moscow"},
				{RegionID: 112, Topology: "moscow-2"},
			},
			legacyRegionID: 10,
			role:           "node",
			nodeTopology:   "moscow-2",
		},
		{
			name:           "duplicate region IDs",
			regions:        []regionConfig{{RegionID: 10, Topology: "moscow"}, {RegionID: 10, Topology: "moscow-2"}},
			legacyRegionID: 10,
			role:           "controller",
			wantErr:        true,
		},
		{
			name:           "duplicate topology labels",
			regions:        []regionConfig{{RegionID: 10, Topology: "moscow"}, {RegionID: 112, Topology: "moscow"}},
			legacyRegionID: 10,
			role:           "controller",
			wantErr:        true,
		},
		{
			name:           "multi-region entry needs topology",
			regions:        []regionConfig{{RegionID: 10, Topology: "moscow"}, {RegionID: 112}},
			legacyRegionID: 10,
			role:           "controller",
			wantErr:        true,
		},
		{
			name:           "legacy region must be configured",
			regions:        []regionConfig{{RegionID: 10, Topology: "moscow"}},
			legacyRegionID: 112,
			role:           "controller",
			wantErr:        true,
		},
		{
			name: "multi-region configuration needs a legacy region",
			regions: []regionConfig{
				{RegionID: 10, Topology: "moscow"},
				{RegionID: 112, Topology: "moscow-2"},
			},
			role:    "controller",
			wantErr: true,
		},
		{
			name:           "multi-region node needs local topology",
			regions:        []regionConfig{{RegionID: 10, Topology: "moscow"}, {RegionID: 112, Topology: "moscow-2"}},
			legacyRegionID: 10,
			role:           "node",
			wantErr:        true,
		},
		{
			name:           "node topology must be configured",
			regions:        []regionConfig{{RegionID: 10, Topology: "moscow"}},
			legacyRegionID: 10,
			role:           "node",
			nodeTopology:   "unknown",
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRegions(tt.regions, tt.legacyRegionID, tt.role, tt.nodeTopology)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateRegions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
