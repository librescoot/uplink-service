package protocol

import "time"

type MessageType string

const (
	// Client-to-server messages.
	MsgTypeAuth            MessageType = "auth"
	MsgTypeState           MessageType = "state"
	MsgTypeChange          MessageType = "change"
	MsgTypeTelemetryDelta  MessageType = "telemetry_delta"
	MsgTypeTelemetryBatch  MessageType = "telemetry_batch"
	MsgTypeEvent           MessageType = "event"
	MsgTypeKeepalive       MessageType = "keepalive"
	MsgTypeCommandResponse MessageType = "command_response"

	// Server-to-client messages.
	MsgTypeAuthResponse MessageType = "auth_response"
	MsgTypeCommand      MessageType = "command"
	MsgTypeConfigUpdate MessageType = "config_update"
)

type BaseMessage struct {
	Type      MessageType `json:"type"`
	Timestamp string      `json:"timestamp"`
}

type AuthMessage struct {
	Type            MessageType `json:"type"`
	Client          string      `json:"client"`
	Version         string      `json:"version"`
	Identifier      string      `json:"identifier"`
	Token           string      `json:"token"`
	ProtocolVersion int         `json:"protocol_version"`
	Timestamp       string      `json:"timestamp"`
}

type AuthResponse struct {
	Type       MessageType `json:"type"`
	Status     string      `json:"status"`
	Error      string      `json:"error,omitempty"`
	ServerTime string      `json:"server_time"`
}

type StateMessage struct {
	Type      MessageType    `json:"type"`
	Data      map[string]any `json:"data"`
	Timestamp string         `json:"timestamp"`
}

type ChangeMessage struct {
	Type      MessageType    `json:"type"`
	Changes   map[string]any `json:"changes"`
	Timestamp string         `json:"timestamp"`
}

// Removed lets the receiver delete leaves a merge cannot remove.
// TelemetryDeltaMessage carries dotted removed paths because a merge alone
// cannot express deletion in the wire protocol.
type TelemetryDeltaMessage struct {
	Type      MessageType    `json:"type"`
	Changes   map[string]any `json:"changes"`
	Removed   []string       `json:"removed,omitempty"`
	Timestamp string         `json:"timestamp"`
}

// TelemetrySnapshot preserves its collection timestamp during offline replay.
type TelemetrySnapshot struct {
	Data      map[string]any `json:"data"`
	Timestamp string         `json:"timestamp"`
}

type TelemetryBatchMessage struct {
	Type      MessageType         `json:"type"`
	Snapshots []TelemetrySnapshot `json:"snapshots"`
	Timestamp string              `json:"timestamp"`
}

type ConfigUpdateMessage struct {
	Type      MessageType       `json:"type"`
	Deltas    map[string]string `json:"deltas"`
	Restart   bool              `json:"restart,omitempty"`
	Timestamp string            `json:"timestamp"`
}

type EventMessage struct {
	Type      MessageType    `json:"type"`
	Event     string         `json:"event"`
	Data      map[string]any `json:"data"`
	Timestamp string         `json:"timestamp"`
}

type KeepaliveMessage struct {
	Type      MessageType `json:"type"`
	Timestamp string      `json:"timestamp"`
}

// CommandMessage is a server-originated request; RequestID is echoed unchanged.
type CommandMessage struct {
	Type      MessageType    `json:"type"`
	RequestID string         `json:"request_id"`
	Command   string         `json:"command"`
	Params    map[string]any `json:"params,omitempty"`
	Timestamp string         `json:"timestamp"`
}

// CommandResponse correlates exactly to the remote request ID.
type CommandResponse struct {
	Type      MessageType    `json:"type"`
	RequestID string         `json:"request_id"`
	Status    string         `json:"status"`
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	Timestamp string         `json:"timestamp"`
}

// Timestamp is RFC3339 UTC for every protocol message.
func Timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
