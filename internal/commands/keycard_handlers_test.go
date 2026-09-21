package commands

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

func TestKeycardCommandError(t *testing.T) {
	if err := keycardCommandError(keycardCommandResult{result: "ok"}); err != nil {
		t.Fatalf("success response returned an error: %v", err)
	}
	if err := keycardCommandError(keycardCommandResult{result: "error:not found", code: "not-found"}); err == nil {
		t.Fatal("error response succeeded")
	} else if !strings.Contains(err.Error(), "error:not found") {
		t.Errorf("error = %q", err)
	}
	if err := keycardCommandError(keycardCommandResult{result: "count:1"}); err == nil {
		t.Fatal("unexpected response succeeded")
	}
}

func TestKeycardCommandsAreSerialized(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	h := &Handler{keycardWatcher: &ipc.HashWatcher{}}
	h.keycardSend = func(command string) error {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
			h.deliverKeycardResult(keycardCommandResult{result: "ok"})
		}()
		return nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, command := range []string{"add:00112233", "remove:00112233"} {
		wg.Add(1)
		go func(command string) {
			defer wg.Done()
			errs <- h.keycardCommand(context.Background(), command)
		}(command)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("keycard command: %v", err)
		}
	}
	if maximum.Load() != 1 {
		t.Errorf("maximum concurrent keycard requests = %d, want 1", maximum.Load())
	}
}
