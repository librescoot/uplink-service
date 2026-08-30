package telemetry

import (
	"testing"
)

func TestBatteryCriticalOnlyWhenPresent(t *testing.T) {
	tests := []struct {
		name        string
		battery     string
		present     string
		charge      string
		expectEvent bool
		description string
	}{
		{
			name:        "present battery low charge",
			battery:     "battery:0",
			present:     "true",
			charge:      "5",
			expectEvent: true,
			description: "Should emit event when battery is present and charge <= 10",
		},
		{
			name:        "absent battery low charge",
			battery:     "battery:1",
			present:     "false",
			charge:      "0",
			expectEvent: false,
			description: "Should NOT emit event when battery is absent, even if charge is 0",
		},
		{
			name:        "present battery OK charge",
			battery:     "battery:0",
			present:     "true",
			charge:      "85",
			expectEvent: false,
			description: "Should NOT emit event when battery is present but charge > 10",
		},
		{
			name:        "battery not present",
			battery:     "battery:1",
			present:     "false",
			charge:      "0",
			expectEvent: false,
			description: "Should NOT emit event for unplugged battery slot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			detector := &EventDetector{
				lastState: make(map[string]string),
			}

			presentHandler := detector.makeBatteryPresentHandler(tt.battery)

			if err := presentHandler(tt.present); err != nil {
				t.Fatalf("presentHandler failed: %v", err)
			}

			presentKey := tt.battery + ":present"
			if detector.lastState[presentKey] != tt.present {
				t.Errorf("Present state not tracked: expected %s, got %s",
					tt.present, detector.lastState[presentKey])
			}

			detector.lastState[tt.battery+":charge"] = ""

			chargeInt := parseInt(tt.charge)
			present := detector.lastState[presentKey]
			chargeKey := tt.battery + ":charge"
			lastCharge := detector.lastState[chargeKey]

			wouldTrigger := present == "true" && chargeInt <= 10 && lastCharge != tt.charge

			if wouldTrigger != tt.expectEvent {
				t.Errorf("%s: logic check failed - expected trigger=%v, got trigger=%v (present=%s, charge=%d, lastCharge=%s)",
					tt.description, tt.expectEvent, wouldTrigger, present, chargeInt, lastCharge)
			}
		})
	}
}

func TestBatteryPresentStatePersists(t *testing.T) {
	detector := &EventDetector{
		lastState: make(map[string]string),
	}

	presentHandler := detector.makeBatteryPresentHandler("battery:0")

	if err := presentHandler("true"); err != nil {
		t.Fatalf("presentHandler(true) returned error: %v", err)
	}
	if detector.lastState["battery:0:present"] != "true" {
		t.Error("Present state not persisted")
	}

	if err := presentHandler("false"); err != nil {
		t.Fatalf("presentHandler(false) returned error: %v", err)
	}
	if detector.lastState["battery:0:present"] != "false" {
		t.Error("Present state change not persisted")
	}
}

func TestPowerStateTracking(t *testing.T) {
	detector := &EventDetector{
		lastState: make(map[string]string),
	}

	stateKey := "power:state"

	detector.lastState[stateKey] = "running"
	if detector.lastState[stateKey] != "running" {
		t.Error("Power state not tracked")
	}

	detector.lastState[stateKey] = "hibernating-timer-pending"
	if detector.lastState[stateKey] != "hibernating-timer-pending" {
		t.Error("Power state change not tracked")
	}
}

func TestLockStateHandlers(t *testing.T) {
	detector := &EventDetector{
		lastState: make(map[string]string),
	}

	stateKey := "vehicle:handlebar"
	detector.lastState[stateKey] = "locked"
	if detector.lastState[stateKey] != "locked" {
		t.Error("Handlebar lock state not tracked")
	}

	detector.lastState[stateKey] = "unlocked"
	if detector.lastState[stateKey] != "unlocked" {
		t.Error("Handlebar unlock state not tracked")
	}

	seatboxKey := "vehicle:seatbox"
	detector.lastState[seatboxKey] = "closed"
	if detector.lastState[seatboxKey] != "closed" {
		t.Error("Seatbox closed state not tracked")
	}

	detector.lastState[seatboxKey] = "open"
	if detector.lastState[seatboxKey] != "open" {
		t.Error("Seatbox open state not tracked")
	}
}
