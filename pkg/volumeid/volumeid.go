package volumeid

import (
	"fmt"
	"strconv"
	"strings"
)

const handlePrefix = "ec-csi-v1"

// Encode adds the EdgeCenter region to a provider resource ID. CSI IDs must be
// globally unique even when the provider scopes IDs to a region.
func Encode(regionID int64, providerID string) string {
	return fmt.Sprintf("%s:%d:%s", handlePrefix, regionID, providerID)
}

// Decode returns the region and provider ID from a CSI volume or snapshot ID.
func Decode(handle string) (int64, string, error) {
	parts := strings.SplitN(handle, ":", 3)
	if len(parts) != 3 || parts[0] != handlePrefix || parts[2] == "" {
		return 0, "", fmt.Errorf("invalid EdgeCenter CSI handle %q", handle)
	}
	regionID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || regionID <= 0 {
		return 0, "", fmt.Errorf("invalid EdgeCenter region in CSI handle %q", handle)
	}
	return regionID, parts[2], nil
}

// ProviderID strips the CSI region prefix before matching the attached device.
func ProviderID(handle string) (string, error) {
	_, providerID, err := Decode(handle)
	return providerID, err
}
