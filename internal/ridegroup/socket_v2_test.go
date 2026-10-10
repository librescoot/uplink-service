package ridegroup

import (
	"context"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/librescoot/uplink-service/internal/config"
	"github.com/librescoot/uplink-service/internal/connection"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestV2ActualSocketAdapterJournalGPSAndDisconnect(t *testing.T) {
	frames := make(chan []byte, 64)
	sockets := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer socket.Close()
		if _, _, err = socket.ReadMessage(); err != nil {
			return
		}
		_ = socket.WriteJSON(map[string]string{"type": "auth_response", "status": "success"})
		sockets <- socket
		for {
			_, body, err := socket.ReadMessage()
			if err != nil {
				return
			}
			frames <- body
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := newTestRedis(t)
	controls := r.client.Subscribe(ctx, controlChannel)
	defer controls.Close()
	if _, err := controls.Receive(ctx); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewAdapter(ctx, Config{Enabled: true, Identifier: "local-test", ProtocolVersion: 2}, r.client)
	if err != nil {
		t.Fatal(err)
	}
	adapter.Start()
	defer adapter.Stop()
	manager := connection.NewManager(&config.Config{Uplink: config.UplinkConfig{ServerURL: "ws" + strings.TrimPrefix(server.URL, "http"), KeepaliveInterval: "1h", ReconnectMaxDelay: "1s"}}, "test")
	var client *connection.LiveClient
	manager.StatusCallback = func(up bool) {
		if up {
			client = manager.LiveClient()
			adapter.OnConnected(client)
		} else {
			adapter.OnDisconnected(client)
		}
	}
	if err = manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	receive := func(kind string) []byte {
		t.Helper()
		for {
			select {
			case body := <-frames:
				var header struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(body, &header)
				if header.Type == kind {
					return body
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("socket did not emit %s", kind)
				return nil
			}
		}
	}
	hello, err := ParseHelloV2(receive(v2HelloType))
	if err != nil {
		t.Fatal(err)
	}
	timeout, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	var probe LocalStopProbeV2
	for {
		message, err := controls.ReceiveMessage(timeout)
		if err != nil {
			t.Fatal(err)
		}
		if value, err := ParseLocalStopProbeV2([]byte(message.Payload)); err == nil {
			probe = value
			break
		}
	}
	if probe.Adapter != "uplink" {
		t.Fatal("wrong adapter probe")
	}
	publish := func(value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.client.Publish(ctx, activityChannel, data).Err(); err != nil {
			t.Fatal(err)
		}
	}
	publish(LocalStopV2{Version: 2, Type: v2LocalStopType, JournalRevision: 1, Phase: "idle", Challenge: probe.Challenge})
	waitFor(t, func() bool { adapter.mu.Lock(); defer adapter.mu.Unlock(); return adapter.journalReady })
	activity := ActivityV2{Version: 2, Type: v2ActivityType, BootNonce: testBoot, Sequence: 1, Active: true, Challenge: testChallenge}
	publish(activity)
	receive(v2ActivityType)
	grant := v2Grant(t)
	grant.ConnectionNonce = hello.ConnectionNonce
	grant.ServerTimeMS = time.Now().UnixMilli()
	socket := <-sockets
	if err = socket.WriteJSON(grant); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { adapter.mu.Lock(); defer adapter.mu.Unlock(); return adapter.grantV2 != nil })
	time.Sleep(time.Second)
	activity.Sequence = 2
	activity.SessionID = strPtr(testSession)
	activity.MemberID = strPtr(testMember)
	activity.Challenge = "00000000-0000-4000-8000-000000000023"
	publish(activity)
	receive(v2ActivityType)
	captured := time.Now().UTC()
	data, _ := json.Marshal(testGPSFix("52.0", "13.0", captured, 3))
	if err = r.client.Publish(ctx, gpsChannel, data).Err(); err != nil {
		t.Fatal(err)
	}
	position, err := ParsePositionV2(receive(v2PositionType))
	if err != nil {
		t.Fatal(err)
	}
	if position.CapturedAtMS != captured.UnixMilli() {
		t.Fatal("source timestamp mismatch")
	}
	invalid := testGPSFix("52.0", "13.0", captured.Add(time.Millisecond), 3)
	invalid.Active = false
	data, _ = json.Marshal(invalid)
	_ = r.client.Publish(ctx, gpsChannel, data).Err()
	status, err := ParseSourceStatusV2(receive(v2SourceStatusType))
	if err != nil || status.State != "no_fix" {
		t.Fatal("source did not close on no-fix", err)
	}
	cancel()
	waitFor(t, func() bool { adapter.mu.Lock(); defer adapter.mu.Unlock(); return adapter.grantV2 == nil })
}
