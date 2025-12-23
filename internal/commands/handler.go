package commands

import (
	"context"
	"fmt"
	"log"
	"time"

	ipc "github.com/librescoot/redis-ipc"

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
	ctx       context.Context
}

// NewHandler creates a new command handler
func NewHandler(connMgr *connection.Manager, client *ipc.Client, collector StateCollector) *Handler {
	return &Handler{
		connMgr:   connMgr,
		client:    client,
		collector: collector,
	}
}

// Start begins handling commands
func (h *Handler) Start(ctx context.Context) {
	h.ctx = ctx
	log.Println("[CommandHandler] Starting...")
	go h.handleLoop()
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

// executeCommand executes a command and sends response
func (h *Handler) executeCommand(cmd *protocol.CommandMessage) {
	log.Printf("[CommandHandler] Executing: %s (req_id=%s)", cmd.Command, cmd.RequestID)

	var err error
	switch cmd.Command {
	// State commands
	case "unlock":
		err = h.sendCommand("scooter:state", "unlock")
	case "lock":
		err = h.sendCommand("scooter:state", "lock")
	case "lock_hibernate":
		err = h.sendCommand("scooter:state", "lock-hibernate")
	case "force_lock":
		err = h.sendCommand("scooter:state", "force-lock")

	// Seatbox commands
	case "open_seatbox":
		err = h.sendCommand("scooter:seatbox", "open")

	// Horn commands
	case "honk":
		err = h.honk(cmd.Params)

	// Blinker commands
	case "blinker_left":
		err = h.sendCommand("scooter:blinker", "left")
	case "blinker_right":
		err = h.sendCommand("scooter:blinker", "right")
	case "blinker_both":
		err = h.sendCommand("scooter:blinker", "both")
	case "blinker_off":
		err = h.sendCommand("scooter:blinker", "off")

	// Hardware commands
	case "dashboard_on":
		err = h.sendCommand("scooter:hardware", "dashboard:on")
	case "dashboard_off":
		err = h.sendCommand("scooter:hardware", "dashboard:off")
	case "engine_on":
		err = h.sendCommand("scooter:hardware", "engine:on")
	case "engine_off":
		err = h.sendCommand("scooter:hardware", "engine:off")
	case "handlebar_lock":
		err = h.sendCommand("scooter:hardware", "handlebar:lock")
	case "handlebar_unlock":
		err = h.sendCommand("scooter:hardware", "handlebar:unlock")

	// Power commands
	case "reboot":
		err = h.sendCommand("scooter:power", "reboot")
	case "hibernate":
		err = h.sendCommand("scooter:power", "hibernate")
	case "hibernate_manual":
		err = h.sendCommand("scooter:power", "hibernate-manual")

	// Special commands
	case "get_state":
		err = h.sendStateSnapshot()
	case "ping":
		err = nil // Success - no action needed

	default:
		err = fmt.Errorf("unknown command: %s", cmd.Command)
	}

	// Send response
	h.sendResponse(cmd.RequestID, cmd.Command, err)
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
	// Get duration parameter (in milliseconds)
	durationMs, ok := params["duration"].(float64)
	if !ok {
		return fmt.Errorf("missing or invalid duration parameter")
	}

	duration := time.Duration(durationMs) * time.Millisecond
	log.Printf("[CommandHandler] Honking for %v", duration)

	// Turn horn on
	if err := h.sendCommand("scooter:horn", "on"); err != nil {
		return err
	}

	// Wait for duration
	time.Sleep(duration)

	// Turn horn off
	if err := h.sendCommand("scooter:horn", "off"); err != nil {
		return err
	}

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
func (h *Handler) sendResponse(requestID, command string, err error) {
	resp := &protocol.CommandResponse{
		Type:      protocol.MsgTypeCommandResponse,
		RequestID: requestID,
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

	log.Printf("[CommandHandler] Sending response: type=%s req_id=%s status=%s", resp.Type, resp.RequestID, resp.Status)
	if sendErr := h.connMgr.SendCommandResponse(resp); sendErr != nil {
		log.Printf("[CommandHandler] Failed to send response: %v", sendErr)
	}
}
