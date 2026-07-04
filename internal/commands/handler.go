package commands

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/connection"
	"github.com/librescoot/uplink-service/internal/protocol"
)

// StateCollector interface for collecting telemetry state
type StateCollector interface {
	CollectState(ctx context.Context) (map[string]any, error)
}

// Handler receives and executes commands from the server
type Handler struct {
	connMgr   *connection.Manager
	client    *ipc.Client
	collector StateCollector
	cfg       *config.Config
	ctx       context.Context

	// alarmCancel stops an in-progress active-alarm pulse loop, if any.
	// alarmGen identifies the current alarm so a finished goroutine does not
	// clear a newer one.
	alarmMu     sync.Mutex
	alarmCancel context.CancelFunc
	alarmGen    int
}

// NewHandler creates a new command handler
func NewHandler(connMgr *connection.Manager, client *ipc.Client, collector StateCollector, cfg *config.Config) *Handler {
	return &Handler{
		connMgr:   connMgr,
		client:    client,
		collector: collector,
		cfg:       cfg,
	}
}

// Start begins handling commands
func (h *Handler) Start(ctx context.Context) {
	h.ctx = ctx
	log.Println("[CommandHandler] Starting...")
	go h.handleLoop()
	go h.handleConfigUpdates()
}

// handleLoop processes commands from the command channel
func (h *Handler) handleLoop() {
	for {
		select {
		case <-h.ctx.Done():
			return
		case cmd := <-h.connMgr.CommandChannel():
			h.executeCommand(cmd)
		}
	}
}

// handleConfigUpdates applies server-pushed config deltas.
func (h *Handler) handleConfigUpdates() {
	for {
		select {
		case <-h.ctx.Done():
			return
		case upd := <-h.connMgr.ConfigUpdateChannel():
			h.applyConfigUpdate(upd)
		}
	}
}

func (h *Handler) applyConfigUpdate(upd *protocol.ConfigUpdateMessage) {
	if err := h.cfg.ApplyDeltas(upd.Deltas); err != nil {
		log.Printf("[CommandHandler] Config update apply error: %v", err)
	}
	if err := h.cfg.Save(); err != nil {
		log.Printf("[CommandHandler] Config save error: %v", err)
	}
	log.Printf("[CommandHandler] Applied config update (%d deltas)", len(upd.Deltas))
	if upd.Restart {
		h.restart()
	}
}

// executeCommand gates, dispatches, and responds to a single command.
func (h *Handler) executeCommand(cmd *protocol.CommandMessage) {
	log.Printf("[CommandHandler] Executing: %s (req_id=%s)", cmd.Command, cmd.RequestID)

	// Gating: ping/get_state are always allowed; everything else honors the
	// per-command disable flag, and shell is development-only.
	if cmd.Command != "ping" && cmd.Command != "get_state" && h.cfg.CommandDisabled(cmd.Command) {
		h.sendResponse(cmd.RequestID, cmd.Command, nil, fmt.Errorf("command disabled in config"))
		return
	}
	if cmd.Command == "shell" && !h.cfg.IsDevelopment() {
		h.sendResponse(cmd.RequestID, cmd.Command, nil, fmt.Errorf("command not allowed in this environment"))
		return
	}

	result, err := h.dispatch(cmd)
	h.sendResponse(cmd.RequestID, cmd.Command, result, err)
}

// dispatch runs a command and returns an optional result payload.
func (h *Handler) dispatch(cmd *protocol.CommandMessage) (map[string]any, error) {
	switch cmd.Command {
	// State commands
	case "unlock":
		return nil, h.sendCommand("scooter:state", "unlock")
	case "lock":
		return nil, h.sendCommand("scooter:state", "lock")
	case "lock_hibernate":
		return nil, h.sendCommand("scooter:state", "lock-hibernate")
	case "force_lock":
		return nil, h.sendCommand("scooter:state", "force-lock")

	// Seatbox commands
	case "open_seatbox":
		return nil, h.sendCommand("scooter:seatbox", "open")

	// Horn commands
	case "honk":
		return nil, h.honk(cmd.Params)

	// Blinker commands
	case "blinker_left":
		return nil, h.sendCommand("scooter:blinker", "left")
	case "blinker_right":
		return nil, h.sendCommand("scooter:blinker", "right")
	case "blinker_both":
		return nil, h.sendCommand("scooter:blinker", "both")
	case "blinker_off":
		return nil, h.sendCommand("scooter:blinker", "off")

	// Hardware commands
	case "dashboard_on":
		return nil, h.sendCommand("scooter:hardware", "dashboard:on")
	case "dashboard_off":
		return nil, h.sendCommand("scooter:hardware", "dashboard:off")
	case "engine_on":
		return nil, h.sendCommand("scooter:hardware", "engine:on")
	case "engine_off":
		return nil, h.sendCommand("scooter:hardware", "engine:off")
	case "handlebar_lock":
		return nil, h.sendCommand("scooter:hardware", "handlebar:lock")
	case "handlebar_unlock":
		return nil, h.sendCommand("scooter:hardware", "handlebar:unlock")

	// Power commands
	case "reboot":
		return nil, h.sendCommand("scooter:power", "reboot")
	case "hibernate":
		return nil, h.sendCommand("scooter:power", "hibernate")
	case "hibernate_manual":
		return nil, h.sendCommand("scooter:power", "hibernate-manual")

	// Alarm commands (queue-backed FSM control)
	case "alarm_arm":
		return nil, h.sendCommand("scooter:alarm", "arm")
	case "alarm_disarm":
		return nil, h.sendCommand("scooter:alarm", "disarm")
	case "alarm_enable":
		return nil, h.sendCommand("scooter:alarm", "enable")
	case "alarm_disable":
		return nil, h.sendCommand("scooter:alarm", "disable")
	case "alarm_stop":
		return nil, h.sendCommand("scooter:alarm", "stop")

	// Rich / composite commands
	case "locate":
		return nil, h.locate()
	case "alarm":
		return nil, h.alarmPulse(cmd.Params)
	case "navigate":
		return nil, h.navigate(cmd.Params)
	case "redis":
		return h.redisCommand(cmd.Params)

	// Config management
	case "config:get":
		return h.configGet(cmd.Params)
	case "config:set":
		return h.configSet(cmd.Params)
	case "config:del":
		return nil, h.configDel(cmd.Params)
	case "config:save":
		return nil, h.cfg.Save()

	// Keycard management
	case "keycards:list":
		return h.keycardsList()
	case "keycards:add":
		return nil, h.keycardsAdd(cmd.Params)
	case "keycards:delete":
		return nil, h.keycardsDelete(cmd.Params)
	case "keycards:master_key:get":
		return h.keycardMasterGet()
	case "keycards:master_key:set":
		return nil, h.keycardMasterSet(cmd.Params)

	// Lifecycle
	case "restart":
		h.restart()
		return nil, nil

	// Special commands
	case "get_state":
		return nil, h.sendStateSnapshot()
	case "ping":
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown command: %s", cmd.Command)
	}
}

// sendCommand sends a command to a Redis list queue
func (h *Handler) sendCommand(queue, cmd string) error {
	log.Printf("[CommandHandler] Sending to %s: %s", queue, cmd)
	if err := ipc.SendRequest(h.client, queue, cmd); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	return nil
}

// honk turns on the horn for a duration then turns it off
func (h *Handler) honk(params map[string]any) error {
	durationMs, ok := params["duration"].(float64)
	if !ok {
		// Fall back to a configured default honk duration if provided.
		if def, ok := h.cfg.CommandParam("honk", "duration", nil).(float64); ok {
			durationMs = def
		} else {
			return fmt.Errorf("missing or invalid duration parameter")
		}
	}

	duration := time.Duration(durationMs) * time.Millisecond
	log.Printf("[CommandHandler] Honking for %v", duration)

	if err := h.sendCommand("scooter:horn", "on"); err != nil {
		return err
	}

	go func() {
		select {
		case <-time.After(duration):
		case <-h.ctx.Done():
		}
		if err := h.sendCommand("scooter:horn", "off"); err != nil {
			log.Printf("[CommandHandler] Failed to turn off horn: %v", err)
		}
	}()

	return nil
}

// sendStateSnapshot collects and sends current state
func (h *Handler) sendStateSnapshot() error {
	log.Printf("[CommandHandler] Collecting state snapshot...")
	state, err := h.collector.CollectState(h.ctx)
	if err != nil {
		return fmt.Errorf("failed to collect state: %w", err)
	}

	log.Printf("[CommandHandler] Sending state snapshot with %d top-level keys", len(state))
	if err := h.connMgr.SendState(state); err != nil {
		return fmt.Errorf("failed to send state: %w", err)
	}

	log.Printf("[CommandHandler] State snapshot sent successfully")
	return nil
}

// sendResponse sends a command response back to the server
func (h *Handler) sendResponse(requestID, command string, result map[string]any, err error) {
	resp := &protocol.CommandResponse{
		Type:      protocol.MsgTypeCommandResponse,
		RequestID: requestID,
		Result:    result,
		Timestamp: protocol.Timestamp(),
	}

	if err != nil {
		resp.Status = "failed"
		resp.Error = err.Error()
		log.Printf("[CommandHandler] Command %s (req_id=%s) failed: %v", command, requestID, err)
	} else {
		resp.Status = "success"
		log.Printf("[CommandHandler] Command %s (req_id=%s) succeeded", command, requestID)
	}

	if sendErr := h.connMgr.SendCommandResponse(resp); sendErr != nil {
		log.Printf("[CommandHandler] Failed to send response: %v", sendErr)
	}
}
