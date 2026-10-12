package identity

import (
	"context"
	"github.com/container-storage-interface/spec/lib/go/csi"
)

func New(name, version string) csi.IdentityServer {
	return &Service{Name: name, Version: version}
}

// GetPluginInfo returns metadata of the plugin
func (s *Service) GetPluginInfo(_ context.Context, _ *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          s.Name,
		VendorVersion: s.Version,
	}, nil
}

// GetPluginCapabilities returns available capabilities of the plugin
func (s *Service) GetPluginCapabilities(_ context.Context, _ *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS,
					},
				},
			},
			{
				Type: &csi.PluginCapability_VolumeExpansion_{
					VolumeExpansion: &csi.PluginCapability_VolumeExpansion{
						Type: csi.PluginCapability_VolumeExpansion_ONLINE,
					},
				},
			},
		},
	}, nil
}

// Probe returns the health and readiness of the plugin
func (s *Service) Probe(_ context.Context, _ *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}
