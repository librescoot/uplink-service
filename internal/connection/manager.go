package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/protocol"
)

const (
	initialRetryDelay = 1 * time.Second
	retryMultiplier   = 2.0
	protocolVersion   = 0
)

// Manager manages the connection to the uplink server
type Manager struct {
	config  *config.Config
	version string
	conn    *websocket.Conn
	mu      sync.RWMutex

	// State
	connected     bool
	authenticated bool
	connectTime   time.Time
	lastMessage   time.Time

	// Statistics
	bytesSent     int64
	bytesReceived int64
	messagesSent  int64
	messagesRecv  int64
	telemetrySent int64
	commandsRecv  int64
	disconnects   int
	retryDelay    time.Duration

	// Channels
	sendChan    chan []byte
	receiveChan chan []byte
	cmdChan     chan *protocol.CommandMessage
	done        chan struct{}
}

// NewManager creates a new connection manager
func NewManager(cfg *config.Config, version string) *Manager {
	return &Manager{
		config:      cfg,
		version:     version,
		retryDelay:  initialRetryDelay,
		sendChan:    make(chan []byte, 256),
		receiveChan: make(chan []byte, 256),
		cmdChan:     make(chan *protocol.CommandMessage, 16),
		done:        make(chan struct{}),
	}
}

// Start starts the connection manager
func (m *Manager) Start(ctx context.Context) error {
	log.Printf("[ConnectionManager] Server: %s", m.config.Uplink.ServerURL)
	log.Printf("[ConnectionManager] Identifier: %s", m.config.Scooter.Identifier)

	go m.connectionLoop(ctx)
	return nil
}

// connectionLoop handles connection and reconnection
func (m *Manager) connectionLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("[ConnectionManager] Shutting down...")
			return
		default:
			if err := m.connect(ctx); err != nil {
				log.Printf("[ConnectionManager] Connection failed: %v", err)
				m.markDisconnected()

				delay := m.getRetryDelay()
				log.Printf("[ConnectionManager] Reconnecting in %s...", delay)

				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
					continue
				}
			}
		}
	}
}

// connect establishes a connection and handles it
func (m *Manager) connect(ctx context.Context) error {
	log.Printf("[ConnectionManager] Connecting to %s...", m.config.Uplink.ServerURL)

	dialer := websocket.Dialer{
		EnableCompression: true,
		HandshakeTimeout:  10 * time.Second,
	}
	conn, _, err := dialer.Dial(m.config.Uplink.ServerURL, nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer func() {
		conn.Close()
		m.mu.Lock()
		m.conn = nil
		m.mu.Unlock()
	}()

	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()

	// Authenticate
	if err := m.authenticate(); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	m.markConnected()
	log.Printf("[ConnectionManager] Connected and authenticated")

	// Start message handlers with separate done channels for coordination
	readDone := make(chan struct{})
	stopSignal := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(3)
	go func() {
		defer wg.Done()
		m.readLoop(readDone, stopSignal)
	}()
	go func() {
		defer wg.Done()
		m.writeLoop(stopSignal)
	}()
	go func() {
		defer wg.Done()
		m.keepaliveLoop(stopSignal)
	}()

	// Wait for disconnect or context cancel
	select {
	case <-ctx.Done():
		log.Println("[ConnectionManager] Context cancelled, closing connection")
		close(stopSignal)
	case <-readDone:
		log.Println("[ConnectionManager] Connection closed")
		close(stopSignal)
	}

	// Wait for all goroutines to finish
	wg.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("connection closed")
}

// authenticate sends authentication message and waits for response
func (m *Manager) authenticate() error {
	authMsg := protocol.AuthMessage{
		Type:            protocol.MsgTypeAuth,
		Client:          "librescoot-uplink",
		Version:         m.version,
		Identifier:      m.config.Scooter.Identifier,
		Token:           m.config.Scooter.Token,
		ProtocolVersion: protocolVersion,
		Timestamp:       protocol.Timestamp(),
	}

	data, err := json.Marshal(authMsg)
	if err != nil {
		return fmt.Errorf("marshal auth failed: %w", err)
	}

	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()

	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("write auth failed: %w", err)
	}

	m.addBytesSent(int64(len(data)))

	// Wait for auth response
	_, message, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth response failed: %w", err)
	}

	m.addBytesReceived(int64(len(message)))

	var authResp protocol.AuthResponse
	if err := json.Unmarshal(message, &authResp); err != nil {
		return fmt.Errorf("parse auth response failed: %w", err)
	}

	if authResp.Status != "success" {
		return fmt.Errorf("authentication rejected: %s", authResp.Error)
	}

	m.mu.Lock()
	m.authenticated = true
	m.mu.Unlock()

	return nil
}

// readLoop handles incoming messages
func (m *Manager) readLoop(readDone chan struct{}, stopSignal <-chan struct{}) {
	defer close(readDone)

	for {
		// Check if we should stop
		select {
		case <-stopSignal:
			return
		default:
		}

		m.mu.RLock()
		conn := m.conn
		m.mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[ConnectionManager] Read error: %v", err)
			}
			return
		}

		m.addBytesReceived(int64(len(message)))
		m.incrementMessagesRecv()
		m.updateLastMessage()

		var baseMsg protocol.BaseMessage
		if err := json.Unmarshal(message, &baseMsg); err != nil {
			log.Printf("[ConnectionManager] Failed to parse message: %v", err)
			continue
		}

		switch baseMsg.Type {
		case protocol.MsgTypeKeepalive:
			log.Println("[ConnectionManager] Received keepalive")

		case protocol.MsgTypeCommand:
			var cmdMsg protocol.CommandMessage
			if err := json.Unmarshal(message, &cmdMsg); err != nil {
				log.Printf("[ConnectionManager] Failed to parse command: %v", err)
				continue
			}
			m.incrementCommandsRecv()
			log.Printf("[ConnectionManager] Received command: %s (request_id=%s)", cmdMsg.Command, cmdMsg.RequestID)

			select {
			case m.cmdChan <- &cmdMsg:
			case <-stopSignal:
				return
			default:
				log.Printf("[ConnectionManager] Command channel full, dropping command")
			}

		default:
			log.Printf("[ConnectionManager] Unknown message type: %s", baseMsg.Type)
		}
	}
}

// writeLoop handles outgoing messages
func (m *Manager) writeLoop(stopSignal <-chan struct{}) {
	for {
		select {
		case <-stopSignal:
			// Drain remaining messages to prevent sender goroutines from blocking
			for {
				select {
				case <-m.sendChan:
					// Discard message
				default:
					return
				}
			}
		case data := <-m.sendChan:
			m.mu.RLock()
			conn := m.conn
			connected := m.connected
			m.mu.RUnlock()

			if conn == nil || !connected {
				continue
			}

			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				log.Printf("[ConnectionManager] Write error: %v", err)
				return
			}

			m.addBytesSent(int64(len(data)))
			m.incrementMessagesSent()
		}
	}
}

// keepaliveLoop sends periodic keepalives
func (m *Manager) keepaliveLoop(stopSignal <-chan struct{}) {
	ticker := time.NewTicker(m.config.Uplink.GetKeepaliveInterval())
	defer ticker.Stop()

	for {
		select {
		case <-stopSignal:
			return
		case <-ticker.C:
			// Check if still connected before sending
			if !m.IsConnected() {
				continue
			}

			msg := protocol.KeepaliveMessage{
				Type:      protocol.MsgTypeKeepalive,
				Timestamp: protocol.Timestamp(),
			}

			data, err := json.Marshal(msg)
			if err != nil {
				log.Printf("[ConnectionManager] Failed to marshal keepalive: %v", err)
				continue
			}

			select {
			case m.sendChan <- data:
				log.Println("[ConnectionManager] Sent keepalive")
			case <-stopSignal:
				return
			default:
				log.Println("[ConnectionManager] Send channel full, skipping keepalive")
			}
		}
	}
}

// SendState sends full state snapshot
func (m *Manager) SendState(data map[string]any) error {
	if !m.IsConnected() {
		return fmt.Errorf("not connected")
	}

	msg := protocol.StateMessage{
		Type:      protocol.MsgTypeState,
		Data:      data,
		Timestamp: protocol.Timestamp(),
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal state failed: %w", err)
	}

	select {
	case m.sendChan <- msgData:
		m.incrementTelemetrySent()
		log.Println("[ConnectionManager] Sent state snapshot")
		return nil
	default:
		return fmt.Errorf("send channel full")
	}
}

// SendChange sends field-level deltas
func (m *Manager) SendChange(changes map[string]any) error {
	if !m.IsConnected() {
		return fmt.Errorf("not connected")
	}

	msg := protocol.ChangeMessage{
		Type:      protocol.MsgTypeChange,
		Changes:   changes,
		Timestamp: protocol.Timestamp(),
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal change failed: %w", err)
	}

	select {
	case m.sendChan <- msgData:
		m.incrementTelemetrySent()
		log.Printf("[ConnectionManager] Sent %d changes", len(changes))
		return nil
	default:
		return fmt.Errorf("send channel full")
	}
}

// SendEvent sends critical event
func (m *Manager) SendEvent(eventType string, data map[string]any) error {
	if !m.IsConnected() {
		return fmt.Errorf("not connected")
	}

	msg := protocol.EventMessage{
		Type:      protocol.MsgTypeEvent,
		Event:     eventType,
		Data:      data,
		Timestamp: protocol.Timestamp(),
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal event failed: %w", err)
	}

	select {
	case m.sendChan <- msgData:
		m.incrementTelemetrySent()
		log.Printf("[ConnectionManager] Sent event: %s", eventType)
		return nil
	default:
		return fmt.Errorf("send channel full")
	}
}

// SendCommandResponse sends a response to a server command
func (m *Manager) SendCommandResponse(resp *protocol.CommandResponse) error {
	if !m.IsConnected() {
		return fmt.Errorf("not connected")
	}

	msgData, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal command response failed: %w", err)
	}

	select {
	case m.sendChan <- msgData:
		m.incrementCommandsRecv() // Track command responses
		log.Printf("[ConnectionManager] Sent command response: %s (status=%s)", resp.RequestID, resp.Status)
		return nil
	default:
		return fmt.Errorf("send channel full")
	}
}

// CommandChannel returns the channel for receiving commands
func (m *Manager) CommandChannel() <-chan *protocol.CommandMessage {
	return m.cmdChan
}

// IsConnected returns whether the connection is active
func (m *Manager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected && m.authenticated
}

// GetStats returns connection statistics
func (m *Manager) GetStats() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	uptime := time.Duration(0)
	if m.connected {
		uptime = time.Since(m.connectTime)
	}

	idle := time.Duration(0)
	if !m.lastMessage.IsZero() {
		idle = time.Since(m.lastMessage)
	}

	return map[string]any{
		"connected":       m.connected,
		"authenticated":   m.authenticated,
		"uptime":          uptime.String(),
		"idle":            idle.String(),
		"bytes_sent":      m.bytesSent,
		"bytes_received":  m.bytesReceived,
		"messages_sent":   m.messagesSent,
		"messages_recv":   m.messagesRecv,
		"telemetry_sent":  m.telemetrySent,
		"commands_recv":   m.commandsRecv,
		"disconnects":     m.disconnects,
	}
}

// Helper methods
func (m *Manager) markConnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	m.connectTime = time.Now()
	m.retryDelay = initialRetryDelay
}

func (m *Manager) markDisconnected() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	m.authenticated = false
	m.disconnects++
}

func (m *Manager) getRetryDelay() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	delay := m.retryDelay
	m.retryDelay = time.Duration(float64(m.retryDelay) * retryMultiplier)
	maxDelay := m.config.Uplink.GetReconnectMaxDelay()
	if m.retryDelay > maxDelay {
		m.retryDelay = maxDelay
	}
	return delay
}

func (m *Manager) addBytesSent(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytesSent += n
}

func (m *Manager) addBytesReceived(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytesReceived += n
}

func (m *Manager) incrementMessagesSent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messagesSent++
}

func (m *Manager) incrementMessagesRecv() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messagesRecv++
}

func (m *Manager) incrementTelemetrySent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.telemetrySent++
}

func (m *Manager) incrementCommandsRecv() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commandsRecv++
}

func (m *Manager) updateLastMessage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMessage = time.Now()
}
