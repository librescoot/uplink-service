package ridegroup

import (
	"context"
	"encoding/json"
	"github.com/alicebob/miniredis/v2"
	"github.com/librescoot/uplink-service/internal/ridegroup/wire"
	redis "github.com/redis/go-redis/v9"
	"sync"
	"testing"
	"time"
)

const (
	testConnection = "00000000-0000-4000-8000-000000000004"
	testBoot       = "00000000-0000-4000-8000-000000000005"
	testChallenge  = "00000000-0000-4000-8000-000000000006"
	testSession    = "00000000-0000-4000-8000-000000000002"
	testMember     = "00000000-0000-4000-8000-000000000003"
	testStream     = "00000000-0000-4000-8000-000000000001"
)

type publishRecord struct{ payload []byte }
type mqttFake struct {
	mu        sync.Mutex
	connected bool
	published []publishRecord
}

func (m *mqttFake) IsConnected() bool                                      { return m.connected }
func (m *mqttFake) Subscribe(string, byte, wire.MessageHandler) wire.Token { return wire.Result{} }
func (m *mqttFake) Unsubscribe(...string) wire.Token                       { return wire.Result{} }
func (m *mqttFake) Publish(_ string, _ byte, _ bool, v any) wire.Token {
	return m.PublishLive(v.([]byte), time.Now().Add(time.Second), func() bool { return true })
}
func (m *mqttFake) PublishLive(data []byte, expires time.Time, authorize func() bool) wire.Token {
	if time.Now().Before(expires) && authorize() {
		m.mu.Lock()
		m.published = append(m.published, publishRecord{append([]byte(nil), data...)})
		m.mu.Unlock()
	}
	return wire.Result{}
}

type testRedis struct{ client *redis.Client }

func newTestRedis(t *testing.T) testRedis {
	t.Helper()
	r := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return testRedis{client: c}
}
func cleanupAdapter(t *testing.T, a *Adapter) {
	t.Helper()
	if a.cancel == nil {
		a.ctx, a.cancel = context.WithCancel(context.Background())
	}
	t.Cleanup(a.Stop)
}
func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("live adapter condition timed out")
}
func setAdapterTestClock(a *Adapter, clock *time.Time, now time.Time) {
	a.mu.Lock()
	*clock = now
	a.mu.Unlock()
}
func testGPSFix(lat, lon string, at time.Time, accuracy float64) GPSFix {
	return GPSFix{Latitude: lat, Longitude: lon, Timestamp: at.UTC().Format(time.RFC3339Nano), Active: true, Connected: true, State: "fix-established", Fix: "3d", Eph: &accuracy}
}
func publishLocalTest(t *testing.T, a *Adapter, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	a.handleActivityV2(payload)
}
