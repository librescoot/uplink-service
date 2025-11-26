package commands

import (
	"context"
	"log"

	"github.com/librescoot/uplink-service/internal/connection"
	"github.com/librescoot/uplink-service/internal/protocol"
)

// Handler handles incoming commands from the server
type Handler struct {
	connMgr *connection.Manager
	ctx     context.Context
}

// NewHandler creates a new command handler
func NewHandler(connMgr *connection.Manager) *Handler {
	return &Handler{
		connMgr: connMgr,
	}
}

// Start starts the command handler
func (h *Handler) Start(ctx context.Context) {
	h.ctx = ctx
	log.Println("[CommandHandler] Starting...")

	go h.handleLoop()
}

// handleLoop processes incoming commands
func (h *Handler) handleLoop() {
	for {
		select {
		case <-h.ctx.Done():
			return
		case cmd := <-h.connMgr.CommandChannel():
			if err := h.handleCommand(cmd); err != nil {
				log.Printf("[CommandHandler] Failed to handle command %s: %v", cmd.Command, err)
			}
		}
	}
}

// handleCommand processes a single command
func (h *Handler) handleCommand(cmd *protocol.CommandMessage) error {
	log.Printf("[CommandHandler] Handling command: %s (request_id=%s)", cmd.Command, cmd.RequestID)

	switch cmd.Command {
	case "unlock":
		log.Println("[CommandHandler] Executing unlock command")
		// TODO: Implement actual unlock via librescoot

	case "lock":
		log.Println("[CommandHandler] Executing lock command")
		// TODO: Implement actual lock via librescoot

	case "reboot":
		log.Println("[CommandHandler] Executing reboot command")
		// TODO: Implement actual reboot

	case "ping":
		log.Println("[CommandHandler] Executing ping command")
		// Simple ping/pong

	default:
		log.Printf("[CommandHandler] Unknown command: %s", cmd.Command)
	}

	// TODO: Send command response back to server

	return nil
}
