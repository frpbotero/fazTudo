//go:build !desktop

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// consoleControl carries local requests between console processes without a
// listening socket. The directory is private and every run has a fresh token.
type consoleControl struct {
	dir    string
	token  string
	active atomic.Bool
	done   chan struct{}
	exited chan struct{}
}

type consolePresence struct {
	Token   string    `json:"token"`
	Updated time.Time `json:"updated"`
}

type consoleRequest struct {
	Token  string          `json:"token"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type consoleResponse struct {
	Status int    `json:"status"`
	Body   []byte `json:"body"`
}

func newConsoleControl(configPath, repo string) *consoleControl {
	if configPath == "" || repo == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(repo))
	return &consoleControl{dir: filepath.Join(filepath.Dir(configPath), "control", hex.EncodeToString(sum[:]))}
}

func (c *consoleControl) serving() bool { return c.active.Load() }

func (c *consoleControl) start(runtime *consoleRuntime) error {
	if c.active.Load() {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(c.dir, 0o700); err != nil {
		return err
	}
	token, err := randomConsoleID()
	if err != nil {
		return err
	}
	c.token, c.done, c.exited = token, make(chan struct{}), make(chan struct{})
	if err := c.heartbeat(); err != nil {
		return err
	}
	c.active.Store(true)
	go c.loop(runtime)
	return nil
}

func (c *consoleControl) stop() {
	if !c.active.Swap(false) {
		return
	}
	close(c.done)
	<-c.exited
	_ = os.Remove(filepath.Join(c.dir, "active.json"))
}

func (c *consoleControl) heartbeat() error {
	data, err := json.Marshal(consolePresence{Token: c.token, Updated: time.Now()})
	if err != nil {
		return err
	}
	return writeConsoleFile(filepath.Join(c.dir, "active.json"), data)
}

func (c *consoleControl) loop(runtime *consoleRuntime) {
	defer close(c.exited)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			_ = c.heartbeat()
			c.process(runtime)
		}
	}
}

func (c *consoleControl) process(runtime *consoleRuntime) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "req-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(c.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var req consoleRequest
		if len(data) > 1<<20 || json.Unmarshal(data, &req) != nil || req.Token != c.token {
			_ = os.Remove(path)
			continue
		}
		resp := runtime.requestLocal(req.Method, req.Path, req.Body)
		encoded, err := json.Marshal(resp)
		if err == nil {
			_ = writeConsoleFile(filepath.Join(c.dir, "resp-"+strings.TrimPrefix(entry.Name(), "req-")), encoded)
		}
		_ = os.Remove(path)
	}
}

func (c *consoleControl) remote(method, path string, body []byte) (consoleResponse, error) {
	data, err := os.ReadFile(filepath.Join(c.dir, "active.json"))
	if os.IsNotExist(err) {
		return consoleResponse{}, nil
	}
	if err != nil {
		return consoleResponse{}, err
	}
	var presence consolePresence
	if json.Unmarshal(data, &presence) != nil || presence.Token == "" || time.Since(presence.Updated) > 3*time.Second {
		return consoleResponse{}, nil
	}
	id, err := randomConsoleID()
	if err != nil {
		return consoleResponse{}, err
	}
	reqPath := filepath.Join(c.dir, "req-"+id+".json")
	respPath := filepath.Join(c.dir, "resp-"+id+".json")
	defer os.Remove(reqPath)
	defer os.Remove(respPath)
	req, err := json.Marshal(consoleRequest{Token: presence.Token, Method: method, Path: path, Body: body})
	if err != nil {
		return consoleResponse{}, err
	}
	if err := writeConsoleFile(reqPath, req); err != nil {
		return consoleResponse{}, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(respPath)
		if err == nil {
			var response consoleResponse
			if err := json.Unmarshal(data, &response); err != nil {
				return consoleResponse{}, err
			}
			return response, nil
		}
		if !os.IsNotExist(err) {
			return consoleResponse{}, err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return consoleResponse{}, fmt.Errorf("a execução ativa não respondeu")
}

func (c *consoleControl) hasActive() bool {
	data, err := os.ReadFile(filepath.Join(c.dir, "active.json"))
	if err != nil {
		return false
	}
	var presence consolePresence
	return json.Unmarshal(data, &presence) == nil && presence.Token != "" && time.Since(presence.Updated) <= 3*time.Second
}

func randomConsoleID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func writeConsoleFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".console-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
