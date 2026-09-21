package telemetry

import "testing"

func TestCollectedHashesIncludeCanonicalBoardVersions(t *testing.T) {
	seen := make(map[string]bool, len(collectedHashes))
	for _, hash := range collectedHashes {
		seen[hash] = true
	}

	for _, hash := range []string{
		"version:mdb", "version:dbc", "trip", "trip:counter", "usb",
		"remote-access", "power-manager:busy-services",
	} {
		if !seen[hash] {
			t.Fatalf("collector must include %q", hash)
		}
	}
	// Secrets must not leave the vehicle.
	if seen["settings"] {
		t.Fatal("collector must not include settings")
	}
}
