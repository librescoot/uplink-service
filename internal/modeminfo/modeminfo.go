// Package modeminfo collects static modem identity via ModemManager's mmcli.
// It polls in the background so telemetry collection never blocks on a shell
// out, and degrades to empty values when mmcli or a modem is unavailable.
package modeminfo

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"sync"
	"time"
)

// Info holds the slowly-changing modem identity fields.
type Info struct {
	Manufacturer string
	Model        string
	Revision     string
	IMEI         string
	OwnNumber    string
	HardwareRev  string
}

// Poller periodically refreshes modem identity.
type Poller struct {
	mu       sync.RWMutex
	info     Info
	interval time.Duration
}

// NewPoller creates a poller with the given refresh interval (defaulting to a
// few minutes when zero, since identity rarely changes).
func NewPoller(interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Poller{interval: interval}
}

// Get returns the most recently observed modem identity.
func (p *Poller) Get() Info {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.info
}

// AsFields returns the identity as a map suitable for merging into telemetry,
// omitting empty values.
func (p *Poller) AsFields() map[string]string {
	info := p.Get()
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("manufacturer", info.Manufacturer)
	add("model", info.Model)
	add("revision", info.Revision)
	add("imei", info.IMEI)
	add("own-number", info.OwnNumber)
	add("hardware-revision", info.HardwareRev)
	return out
}

// Start refreshes once immediately, then on the configured interval until the
// context is cancelled.
func (p *Poller) Start(ctx context.Context) {
	p.refresh()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.refresh()
		}
	}
}

func (p *Poller) refresh() {
	info, ok := queryModem()
	if !ok {
		return
	}
	p.mu.Lock()
	p.info = info
	p.mu.Unlock()
}

// mmcliModem mirrors the subset of `mmcli -J -m any` output we consume.
type mmcliModem struct {
	Modem struct {
		Generic struct {
			Manufacturer        string   `json:"manufacturer"`
			Model               string   `json:"model"`
			Revision            string   `json:"revision"`
			HardwareRevision    string   `json:"hardware-revision"`
			EquipmentIdentifier string   `json:"equipment-identifier"`
			OwnNumbers          []string `json:"own-numbers"`
		} `json:"generic"`
		ThreeGPP struct {
			IMEI string `json:"imei"`
		} `json:"3gpp"`
	} `json:"modem"`
}

func queryModem() (Info, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "mmcli", "-J", "-m", "any").Output()
	if err != nil {
		return Info{}, false
	}

	var parsed mmcliModem
	if err := json.Unmarshal(out, &parsed); err != nil {
		log.Printf("[ModemInfo] Failed to parse mmcli output: %v", err)
		return Info{}, false
	}

	g := parsed.Modem.Generic
	info := Info{
		Manufacturer: g.Manufacturer,
		Model:        g.Model,
		Revision:     g.Revision,
		HardwareRev:  g.HardwareRevision,
		IMEI:         parsed.Modem.ThreeGPP.IMEI,
	}
	if info.IMEI == "" {
		info.IMEI = g.EquipmentIdentifier
	}
	if len(g.OwnNumbers) > 0 {
		info.OwnNumber = g.OwnNumbers[0]
	}
	return info, true
}
