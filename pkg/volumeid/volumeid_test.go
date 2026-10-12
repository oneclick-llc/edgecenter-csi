package volumeid

import "testing"

func TestEncodeDecode(t *testing.T) {
	const providerID = "volume:with:colons"
	handle := Encode(112, providerID)
	regionID, decodedID, err := Decode(handle)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if regionID != 112 || decodedID != providerID {
		t.Fatalf("Decode() = (%d, %q), want (112, %q)", regionID, decodedID, providerID)
	}
}

func TestDecodeLegacyProviderID(t *testing.T) {
	const providerID = "legacy-volume-id"
	regionID, decodedID, err := Decode(providerID)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if regionID != 0 || decodedID != providerID {
		t.Fatalf("Decode() = (%d, %q), want (0, %q)", regionID, decodedID, providerID)
	}
}

func TestDecodeRejectsInvalidRegionalHandles(t *testing.T) {
	for _, handle := range []string{
		"",
		handlePrefix + ":0:volume",
		handlePrefix + ":not-a-region:volume",
		handlePrefix + ":10:",
	} {
		t.Run(handle, func(t *testing.T) {
			if _, _, err := Decode(handle); err == nil {
				t.Fatalf("Decode(%q) succeeded, want error", handle)
			}
		})
	}
}

func TestProviderID(t *testing.T) {
	for _, tt := range []struct {
		name   string
		handle string
		want   string
	}{
		{name: "regional", handle: Encode(10, "volume-id"), want: "volume-id"},
		{name: "legacy", handle: "volume-id", want: "volume-id"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProviderID(tt.handle)
			if err != nil {
				t.Fatalf("ProviderID() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ProviderID() = %q, want %q", got, tt.want)
			}
		})
	}
}
