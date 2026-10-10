package connection

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/ridegroup/wire"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveQueueLatestOnlyAndFinalAuthorization(t *testing.T) {
	received := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			received <- data
		}
	}))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	m := NewManager(&config.Config{}, "test")
	m.conn, m.connected, m.authenticated = conn, true, true
	client := m.LiveClient()
	for n := 1; n <= 5; n++ {
		data, _ := json.Marshal(map[string]any{"type": "ride_group_position", "seq": n})
		if err := client.PublishLive(data, time.Now().Add(time.Second), func() bool { return true }).Error(); err != nil {
			t.Fatal(err)
		}
	}
	m.liveMu.Lock()
	count := len(m.livePending)
	m.liveMu.Unlock()
	if count != 1 {
		t.Fatal("live positions entered a backlog")
	}
	frame, ok := m.takeLive()
	if !ok || !m.writeLive(frame) {
		t.Fatal("latest write failed")
	}
	select {
	case data := <-received:
		var v map[string]any
		_ = json.Unmarshal(data, &v)
		if v["seq"] != float64(5) {
			t.Fatal("older point written")
		}
	case <-time.After(time.Second):
		t.Fatal("no actual socket delivery")
	}
	data := []byte(`{"type":"ride_group_position","seq":6}`)
	client.PublishLive(data, time.Now().Add(time.Second), func() bool { return false })
	frame, _ = m.takeLive()
	if !m.writeLive(frame) {
		t.Fatal("denial disconnected writer")
	}
	select {
	case <-received:
		t.Fatal("unauthorized point written")
	case <-time.After(30 * time.Millisecond):
	}
	client.PublishLive(data, time.Now().Add(time.Second), func() bool { return true })
	frame, _ = m.takeLive()
	frame.expires = time.Now().Add(-time.Second)
	m.writeLive(frame)
	select {
	case <-received:
		t.Fatal("expired point written")
	case <-time.After(30 * time.Millisecond):
	}
	m.mu.Lock()
	m.conn = nil
	m.mu.Unlock()
	frame.expires = time.Now().Add(time.Second)
	m.writeLive(frame)
	select {
	case <-received:
		t.Fatal("old connection point written")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestLiveAuthenticationDispatchAndWriterShareOneSocket(t *testing.T) {
	received := make(chan []byte, 8)
	serverConn := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err = c.ReadMessage(); err != nil {
			return
		}
		_ = c.WriteJSON(map[string]string{"type": "auth_response", "status": "success"})
		serverConn <- c
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			received <- data
		}
	}))
	defer server.Close()
	cfg := &config.Config{Uplink: config.UplinkConfig{ServerURL: "ws" + strings.TrimPrefix(server.URL, "http"), KeepaliveInterval: "1h", ReconnectMaxDelay: "1s"}}
	m := NewManager(cfg, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan *LiveClient, 1)
	m.StatusCallback = func(up bool) {
		if up {
			connected <- m.LiveClient()
		}
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var client *LiveClient
	select {
	case client = <-connected:
	case <-time.After(time.Second):
		t.Fatal("no authenticated client")
	}
	incoming := make(chan []byte, 1)
	client.Subscribe("", 0, func(_ wire.Client, message wire.Message) { incoming <- append([]byte(nil), message.Payload()...) })
	socket := <-serverConn
	if err := socket.WriteJSON(map[string]any{"type": "ride_group_control", "v": 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-incoming:
	case <-time.After(time.Second):
		t.Fatal("live dispatch missed authenticated socket")
	}
	if err := client.PublishLive([]byte(`{"type":"ride_group_position","seq":1}`), time.Now().Add(time.Second), func() bool { return true }).Error(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("live writer missed authenticated socket")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for m.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.IsConnected() {
		t.Fatal("cancellation left connection/read loop alive")
	}
}
