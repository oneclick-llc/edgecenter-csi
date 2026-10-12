package node

import (
	"ec-csi-plugin/pkg/volumeid"
	"fmt"
	"path/filepath"
	"strings"
)

// findAbsoluteDeviceByIDPath follows the /dev/disk/by-id symlink to find the absolute path of a device
func (s *Service) findAbsoluteDeviceByIDPath(volumeID string) (string, error) {
	providerID, err := volumeid.ProviderID(volumeID)
	if err != nil {
		return "", fmt.Errorf("invalid volume handle: %w", err)
	}
	path, err := s.mounter.DevicePathByVolumeID(providerID)
	if err != nil {
		return "", fmt.Errorf("cannot find device path: %w", err)
	}
	// EvalSymlinks returns relative link if the file is not a symlink,
	// so we do not have to check if it is symlink prior to evaluation
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("could not resolve symlink %q: %v", path, err)
	}

	if !strings.HasPrefix(resolved, "/dev") {
		return "", fmt.Errorf("resolved symlink %q for %q was unexpected", resolved, path)
	}

	return resolved, nil
}
