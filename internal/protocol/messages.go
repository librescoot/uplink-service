package protocol

import "time"

// MessageType represents the type of message
type MessageType string

const (
	// Client → Server
	MsgTypeAuth            MessageType = "auth"
	MsgTypeState           MessageType = "state"
	MsgTypeChange          MessageType = "change"
	MsgTypeEvent           MessageType = "event"
	MsgTypeKeepalive       MessageType = "keepalive"
	MsgTypeCommandResponse MessageType = "command_response"

	// Server → Client
	MsgTypeAuthResponse MessageType = "auth_response"
	MsgTypeCommand      MessageType = "command"
	MsgTypeConfigUpdate MessageType = "config_update"
)

// BaseMessage is the base structure for all messages
type BaseMessage struct {
	Type      MessageType `json:"type"`
	Timestamp string      `json:"timestamp"`
}

// AuthMessage - Client authenticates with server
type AuthMessage struct {
	Type            MessageType `json:"type"`
	Client          string      `json:"client"`
	Version         string      `json:"version"`
	Identifier      string      `json:"identifier"`
	Token           string      `json:"token"`
	ProtocolVersion int         `json:"protocol_version"`
	Timestamp       string      `json:"timestamp"`
}

// AuthResponse - Server responds to authentication
type AuthResponse struct {
	Type       MessageType `json:"type"`
	Status     string      `json:"status"`
	Error      string      `json:"error,omitempty"`
	ServerTime string      `json:"server_time"`
}

// StateMessage - Client sends full state snapshot
type StateMessage struct {
	Type      MessageType            `json:"type"`
	Data      map[string]any `json:"data"`
	Timestamp string                 `json:"timestamp"`
}

// ChangeMessage - Client sends field-level deltas
type ChangeMessage struct {
	Type      MessageType            `json:"type"`
	Changes   map[string]any `json:"changes"`
	Timestamp string                 `json:"timestamp"`
}

// EventMessage - Client sends critical event
type EventMessage struct {
	Type      MessageType            `json:"type"`
	Event     string                 `json:"event"`
	Data      map[string]any `json:"data"`
	Timestamp string                 `json:"timestamp"`
}

// KeepaliveMessage - Bidirectional keepalive
type KeepaliveMessage struct {
	Type      MessageType `json:"type"`
	Timestamp string      `json:"timestamp"`
}

// CommandMessage - Server sends command to client
type CommandMessage struct {
	Type      MessageType            `json:"type"`
	RequestID string                 `json:"request_id"`
	Command   string                 `json:"command"`
	Params    map[string]any `json:"params,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// CommandResponse - Client responds to command
type CommandResponse struct {
	Type      MessageType            `json:"type"`
	RequestID string                 `json:"request_id"`
	Status    string                 `json:"status"`
	Result    map[string]any `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Timestamp string                 `json:"timestamp"`
}

// Helper function to create timestamp string
func Timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
