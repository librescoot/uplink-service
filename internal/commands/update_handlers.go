package commands

import "fmt"

// resolveUpdateTargets validates requested update-check boards and applies the
// MDB orchestration rule: when the MDB checks the DBC itself, a both-board
// request is queued on the MDB alone so the DBC is only woken by its preflight.
func resolveUpdateTargets(requested []string, orchestrateDBC bool) ([]string, error) {
	if len(requested) == 0 {
		requested = []string{"mdb", "dbc"}
	}
	valid := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, t := range requested {
		if t != "mdb" && t != "dbc" {
			return nil, fmt.Errorf("invalid target %q (want mdb or dbc)", t)
		}
		if !seen[t] {
			seen[t] = true
			valid = append(valid, t)
		}
	}
	if orchestrateDBC && len(valid) == 2 {
		return []string{"mdb"}, nil
	}
	return valid, nil
}

// updateCheck queues check-now on the boards that own the request. Parameters:
// optional "target" ("mdb" or "dbc") or "targets" (list); both boards when unset.
func (h *Handler) updateCheck(params map[string]any) (map[string]any, error) {
	var requested []string
	switch raw := params["targets"].(type) {
	case []any:
		for _, v := range raw {
			requested = append(requested, fmt.Sprint(v))
		}
	case []string:
		requested = raw
	default:
		if t, ok := params["target"].(string); ok && t != "" {
			requested = []string{t}
		}
	}

	queued, err := resolveUpdateTargets(requested, h.dbcOrchestrationEnabled())
	if err != nil {
		return nil, err
	}
	for _, target := range queued {
		if err := h.sendCommand("scooter:update:"+target, "check-now"); err != nil {
			return nil, err
		}
	}
	return map[string]any{"targets": queued}, nil
}

// dbcOrchestrationEnabled reads settings[updates.mdb.orchestrate-dbc]. An
// absent or unreadable value counts as enabled, matching update-service's
// own default.
func (h *Handler) dbcOrchestrationEnabled() bool {
	val, err := h.client.HGet("settings", "updates.mdb.orchestrate-dbc")
	if err != nil || val == "" {
		return true
	}
	return val == "true"
}
