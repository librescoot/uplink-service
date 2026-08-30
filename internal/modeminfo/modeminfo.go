package modeminfo

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"sync"
	"time"
)

type Info struct {
	Manufacturer string
	Model        string
	Revision     string
	IMEI         string
	OwnNumber    string
	HardwareRev  string
}

type Poller struct {
	mu       sync.RWMutex
	info     Info
	interval time.Duration
}

func NewPoller(interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Poller{interval: interval}
}

func (p *Poller) Get() Info {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.info
}

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

// mmcli may be absent off-target; modem identity is optional telemetry.
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
