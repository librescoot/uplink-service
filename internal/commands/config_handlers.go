package commands

import "fmt"

func (h *Handler) configGet(params map[string]any) (map[string]any, error) {
	if field, ok := params["field"].(string); ok && field != "" {
		v, err := h.cfg.GetField(field)
		if err != nil {
			return nil, err
		}
		return map[string]any{"field": field, "value": v}, nil
	}
	raw, err := h.cfg.RawYAML()
	if err != nil {
		return nil, err
	}
	return map[string]any{"config_yaml": raw}, nil
}

func (h *Handler) configSet(params map[string]any) (map[string]any, error) {
	field, _ := params["field"].(string)
	if field == "" {
		return nil, fmt.Errorf("field is required")
	}
	value := fmt.Sprint(params["value"])
	if err := h.cfg.SetField(field, value); err != nil {
		return nil, err
	}
	return map[string]any{"field": field, "value": value}, nil
}

func (h *Handler) configDel(params map[string]any) error {
	field, _ := params["field"].(string)
	if field == "" {
		return fmt.Errorf("field is required")
	}
	return h.cfg.DeleteField(field)
}
