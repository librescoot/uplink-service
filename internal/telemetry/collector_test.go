package telemetry

import "testing"

func TestCollectedHashesIncludeCanonicalBoardVersions(t *testing.T) {
	seen := make(map[string]bool, len(collectedHashes))
	for _, hash := range collectedHashes {
		seen[hash] = true
	}

	for _, hash := range []string{
		"version:mdb", "version:dbc", "trip", "trip:counter", "usb",
		"remote-access", "power-manager:busy-services", "settings",
	} {
		if !seen[hash] {
			t.Fatalf("collector must include %q", hash)
		}
	}
}

func TestSettingsFieldAllowed(t *testing.T) {
	allowed := []string{
		"updates.mdb.channel",
		"updates.dbc.last-attempt-result",
		"pm.hibernation-timer",
		"alarm.enabled",
		"trip.counter-reset",
		"engine-ecu.kers-power",
		"dashboard.service-mode-active",
		"scooter.developer-mode",
		"scooter.dual-battery",
	}
	for _, field := range allowed {
		if !settingsFieldAllowed(field) {
			t.Errorf("settings field %q must be collected", field)
		}
	}

	denied := []string{
		"cellular.sim-pin",
		"cellular.password",
		"cellular.username",
		"cellular.auth",
		"cellular.apn",
		"dashboard.saved-locations.0.latitude",
		"dashboard.recent-destinations.0.longitude",
		"dashboard.theme",
		"scooter.usb0-policy",
	}
	for _, field := range denied {
		if settingsFieldAllowed(field) {
			t.Errorf("settings field %q must stay on the vehicle", field)
		}
	}
}
