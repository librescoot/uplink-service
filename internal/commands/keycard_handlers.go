package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var keycardDirs = []string{"/data/keycard", "/etc/reunu-keycard"}

const (
	authorizedFile = "authorized_uids.txt"
	masterFile     = "master_uids.txt"
)

func keycardPath(file string) string {
	for _, dir := range keycardDirs {
		if _, err := os.Stat(dir); err == nil {
			return filepath.Join(dir, file)
		}
	}
	return filepath.Join(keycardDirs[0], file)
}

func readUIDs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var uids []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			uids = append(uids, s)
		}
	}
	return uids, nil
}

func writeUIDs(path string, uids []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sort.Strings(uids)
	return os.WriteFile(path, []byte(strings.Join(uids, "\n")+"\n"), 0o600)
}

func (h *Handler) keycardsList() (map[string]any, error) {
	uids, err := readUIDs(keycardPath(authorizedFile))
	if err != nil {
		return nil, err
	}
	return map[string]any{"uids": uids}, nil
}

func (h *Handler) keycardsAdd(params map[string]any) error {
	uid, _ := params["uid"].(string)
	if uid == "" {
		return fmt.Errorf("uid is required")
	}
	path := keycardPath(authorizedFile)
	uids, err := readUIDs(path)
	if err != nil {
		return err
	}
	for _, u := range uids {
		if u == uid {
			return nil
		}
	}
	return writeUIDs(path, append(uids, uid))
}

func (h *Handler) keycardsDelete(params map[string]any) error {
	uid, _ := params["uid"].(string)
	if uid == "" {
		return fmt.Errorf("uid is required")
	}
	path := keycardPath(authorizedFile)
	uids, err := readUIDs(path)
	if err != nil {
		return err
	}
	out := uids[:0]
	for _, u := range uids {
		if u != uid {
			out = append(out, u)
		}
	}
	return writeUIDs(path, out)
}

func (h *Handler) keycardMasterGet() (map[string]any, error) {
	uids, err := readUIDs(keycardPath(masterFile))
	if err != nil {
		return nil, err
	}
	var master string
	if len(uids) > 0 {
		master = uids[0]
	}
	return map[string]any{"master": master}, nil
}

func (h *Handler) keycardMasterSet(params map[string]any) error {
	uid, _ := params["uid"].(string)
	if uid == "" {
		return fmt.Errorf("uid is required")
	}
	return writeUIDs(keycardPath(masterFile), []string{uid})
}
