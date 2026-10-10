package connection

import (
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"github.com/librescoot/uplink-service/internal/ridegroup/wire"
	"time"
)

type liveFrame struct {
	data       []byte
	connection *websocket.Conn
	expires    time.Time
	authorize  func() bool
	order      uint64
}

type LiveClient struct {
	manager    *Manager
	connection *websocket.Conn
}

func (m *Manager) LiveClient() *LiveClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return &LiveClient{manager: m, connection: m.conn}
}
func (c *LiveClient) IsConnected() bool {
	c.manager.mu.RLock()
	defer c.manager.mu.RUnlock()
	return c.connection != nil && c.manager.conn == c.connection && c.manager.connected && c.manager.authenticated
}
func (c *LiveClient) Subscribe(_ string, _ byte, handler wire.MessageHandler) wire.Token {
	c.manager.liveMu.Lock()
	defer c.manager.liveMu.Unlock()
	c.manager.liveReceiver = func(data []byte) { handler(c, wire.Frame(data)) }
	c.manager.liveReceiverClient = c
	return wire.Result{}
}
func (c *LiveClient) Unsubscribe(...string) wire.Token {
	c.manager.liveMu.Lock()
	defer c.manager.liveMu.Unlock()
	if c.manager.liveReceiverClient == c {
		c.manager.liveReceiver = nil
		c.manager.liveReceiverClient = nil
	}
	return wire.Result{}
}
func (c *LiveClient) Publish(_ string, _ byte, _ bool, data any) wire.Token {
	payload, ok := data.([]byte)
	if !ok {
		return wire.Result{Err: errors.New("invalid live frame")}
	}
	return c.PublishLive(payload, time.Now().Add(time.Second), func() bool { return true })
}
func (c *LiveClient) PublishLive(data []byte, expires time.Time, authorize func() bool) wire.Token {
	if len(data) == 0 || len(data) > 1024 || authorize == nil || !c.IsConnected() {
		return wire.Result{Err: errors.New("live transport unavailable")}
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &header) != nil {
		return wire.Result{Err: errors.New("invalid live frame")}
	}
	switch header.Type {
	case "ride_group_hello", "ride_group_activity", "ride_group_position", "ride_group_source_status", "ride_group_stop":
	default:
		return wire.Result{Err: errors.New("invalid live frame type")}
	}
	now := time.Now()
	if expires.IsZero() || expires.After(now.Add(time.Second)) {
		expires = now.Add(time.Second)
	}
	if !now.Before(expires) {
		return wire.Result{Err: errors.New("expired live frame")}
	}
	c.manager.liveMu.Lock()
	if c.manager.livePending == nil {
		c.manager.livePending = make(map[string]liveFrame)
	}
	c.manager.liveOrder++
	c.manager.livePending[header.Type] = liveFrame{data: append([]byte(nil), data...), connection: c.connection, expires: expires, authorize: authorize, order: c.manager.liveOrder}
	c.manager.liveMu.Unlock()
	select {
	case c.manager.liveWake <- struct{}{}:
	default:
	}
	return wire.Result{}
}
func (m *Manager) deliverLive(data []byte) {
	if len(data) > 1024 {
		return
	}
	m.liveMu.Lock()
	receiver, client := m.liveReceiver, m.liveReceiverClient
	m.liveMu.Unlock()
	if receiver != nil && client != nil && client.IsConnected() {
		receiver(append([]byte(nil), data...))
	}
}
func (m *Manager) takeLive() (liveFrame, bool) {
	m.liveMu.Lock()
	defer m.liveMu.Unlock()
	var result liveFrame
	var key string
	for k, v := range m.livePending {
		if key == "" || v.order < result.order {
			key, result = k, v
		}
	}
	if key == "" {
		return result, false
	}
	delete(m.livePending, key)
	if len(m.livePending) > 0 {
		select {
		case m.liveWake <- struct{}{}:
		default:
		}
	}
	return result, true
}
func (m *Manager) resetLiveQueue() {
	m.liveMu.Lock()
	m.livePending = make(map[string]liveFrame)
	m.liveMu.Unlock()
}
func (m *Manager) writeLive(frame liveFrame) bool {
	if !time.Now().Before(frame.expires) || frame.authorize == nil || !frame.authorize() {
		return true
	}
	m.mu.RLock()
	conn, connected := m.conn, m.connected && m.authenticated
	m.mu.RUnlock()
	if conn == nil || !connected || conn != frame.connection || !time.Now().Before(frame.expires) {
		return true
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	if frame.expires.Before(deadline) {
		deadline = frame.expires
	}
	_ = conn.SetWriteDeadline(deadline)
	if err := conn.WriteMessage(websocket.TextMessage, frame.data); err != nil {
		_ = conn.Close()
		return false
	}
	m.addBytesSent(int64(len(frame.data)))
	m.incrementMessagesSent()
	return true
}
