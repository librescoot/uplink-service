package commands

import (
	"context"
	"fmt"
	"log"

	ipc "github.com/librescoot/redis-ipc"

	"github.com/librescoot/uplink-service/internal/connection"
	"github.com/librescoot/uplink-service/internal/protocol"
)

// Handler receives and executes commands from the server
type Handler struct {
	connMgr *connection.Manager
	client  *ipc.Client
	ctx     context.Context
}

// NewHandler creates a new command handler
func NewHandler(connMgr *connection.Manager, client *ipc.Client) *Handler {
	return &Handler{
		connMgr: connMgr,
		client:  client,
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
	case "unlock":
		err = h.sendVehicleCommand("unlock")
	case "lock":
		err = h.sendVehicleCommand("lock")
	case "reboot":
		err = h.sendPowerCommand("reboot")
	case "hibernate":
		err = h.sendPowerCommand("hibernate")
	case "ping":
		err = nil // Success - no action needed
	default:
		err = fmt.Errorf("unknown command: %s", cmd.Command)
	}

	// Send response
	h.sendResponse(cmd.RequestID, cmd.Command, err)
}

// sendVehicleCommand sends a state command to the vehicle-service queue
func (h *Handler) sendVehicleCommand(cmd string) error {
	log.Printf("[CommandHandler] Sending vehicle command: %s", cmd)
	if err := ipc.SendRequest(h.client, "scooter:state", cmd); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	log.Printf("[CommandHandler] Command sent successfully: %s", cmd)
	return nil
}

// sendPowerCommand sends a power command to the pm-service queue
func (h *Handler) sendPowerCommand(cmd string) error {
	log.Printf("[CommandHandler] Sending power command: %s", cmd)
	if err := ipc.SendRequest(h.client, "scooter:power", cmd); err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	log.Printf("[CommandHandler] Power command sent successfully: %s", cmd)
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
