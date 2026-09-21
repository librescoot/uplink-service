package commands

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

const (
	keycardCommandQueue   = "scooter:keycard"
	keycardHash           = "keycard"
	keycardAuthorizedSet  = "keycard:authorized"
	keycardMastersSet     = "keycard:masters"
	keycardCommandTimeout = 5 * time.Second
)

type keycardCommandResult struct {
	result string
	code   string
	err    error
}

func (h *Handler) startKeycardWatcher(ctx context.Context) {
	watcher := h.client.NewHashWatcher(keycardHash)
	watcher.OnField("command-result", h.handleKeycardResult)
	if err := watcher.Start(); err != nil {
		log.Printf("[CommandHandler] Failed to watch keycard results: %v", err)
		return
	}
	h.keycardWatcher = watcher
	go func() {
		<-ctx.Done()
		if err := watcher.Stop(); err != nil {
			log.Printf("[CommandHandler] Failed to stop keycard result watcher: %v", err)
		}
	}()
}

func (h *Handler) handleKeycardResult(result string) error {
	code, err := h.client.Hash(keycardHash).Get("command-error")
	if err != nil {
		return fmt.Errorf("read keycard command error: %w", err)
	}
	h.deliverKeycardResult(keycardCommandResult{result: result, code: code})
	return nil
}

func (h *Handler) deliverKeycardResult(result keycardCommandResult) {
	h.keycardResultMu.Lock()
	response := h.keycardResult
	h.keycardResultMu.Unlock()
	if response == nil {
		return
	}
	select {
	case response <- result:
	default:
	}
}

func (h *Handler) keycardCommand(ctx context.Context, command string) error {
	h.keycardMu.Lock()
	defer h.keycardMu.Unlock()

	if h.keycardWatcher == nil {
		return fmt.Errorf("keycard result watcher is unavailable")
	}
	response := make(chan keycardCommandResult, 1)
	h.keycardResultMu.Lock()
	h.keycardResult = response
	h.keycardResultMu.Unlock()
	defer func() {
		h.keycardResultMu.Lock()
		if h.keycardResult == response {
			h.keycardResult = nil
		}
		h.keycardResultMu.Unlock()
	}()

	if err := h.sendKeycardCommand(command); err != nil {
		return fmt.Errorf("send keycard command: %w", err)
	}

	timer := time.NewTimer(keycardCommandTimeout)
	defer timer.Stop()
	select {
	case result := <-response:
		if result.err != nil {
			return result.err
		}
		return keycardCommandError(result)
	case <-timer.C:
		return fmt.Errorf("timed out waiting for keycard command response")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) sendKeycardCommand(command string) error {
	if h.keycardSend != nil {
		return h.keycardSend(command)
	}
	return ipc.SendRequest(h.client, keycardCommandQueue, command)
}

func keycardCommandError(result keycardCommandResult) error {
	if result.code != "" {
		return fmt.Errorf("keycard command failed: %s", result.result)
	}
	if result.result != "ok" {
		return fmt.Errorf("unexpected keycard command response: %s", result.result)
	}
	return nil
}

func (h *Handler) keycardsList() (map[string]any, error) {
	uids, err := h.client.Raw().SMembers(context.Background(), keycardAuthorizedSet).Result()
	if err != nil {
		return nil, fmt.Errorf("read authorized keycards: %w", err)
	}
	sort.Strings(uids)
	return map[string]any{"uids": uids}, nil
}

func (h *Handler) keycardsAdd(params map[string]any) error {
	uid, _ := params["uid"].(string)
	if uid == "" {
		return fmt.Errorf("uid is required")
	}
	return h.keycardCommand(h.commandContext(), "add:"+uid)
}

func (h *Handler) keycardsDelete(params map[string]any) error {
	uid, _ := params["uid"].(string)
	if uid == "" {
		return fmt.Errorf("uid is required")
	}
	return h.keycardCommand(h.commandContext(), "remove:"+uid)
}

func (h *Handler) keycardMasterGet() (map[string]any, error) {
	masters, err := h.client.Raw().SMembers(context.Background(), keycardMastersSet).Result()
	if err != nil {
		return nil, fmt.Errorf("read master keycards: %w", err)
	}
	sort.Strings(masters)
	master := ""
	if len(masters) > 0 {
		master = masters[0]
	}
	return map[string]any{"master": master}, nil
}

func (h *Handler) keycardMasterSet(params map[string]any) error {
	uid, _ := params["uid"].(string)
	if strings.TrimSpace(uid) == "" {
		return fmt.Errorf("uid is required")
	}
	return h.keycardCommand(h.commandContext(), "set-master:"+uid)
}

func (h *Handler) commandContext() context.Context {
	if h.ctx != nil {
		return h.ctx
	}
	return context.Background()
}
