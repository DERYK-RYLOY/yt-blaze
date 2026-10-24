package fastdl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// aria2Client is a minimal JSON-RPC 2.0 client for the aria2c daemon.
type aria2Client struct {
	url    string // http://127.0.0.1:PORT/jsonrpc
	secret string
	http   *http.Client

	mu     sync.Mutex
	nextID int
}

func newAria2Client(port int, secret string) *aria2Client {
	return &aria2Client{
		url:    fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port),
		secret: secret,
		http:   &http.Client{Timeout: 10 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *aria2Client) call(ctx context.Context, method string, params ...interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := strconv.Itoa(c.nextID)
	c.mu.Unlock()

	all := make([]interface{}, 0, len(params)+1)
	if c.secret != "" {
		all = append(all, "token:"+c.secret)
	}
	all = append(all, params...)

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: all})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("aria2 rpc error %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

// AddURI starts a download and returns the gid.
func (c *aria2Client) AddURI(ctx context.Context, uri string, opts map[string]string) (string, error) {
	options := map[string]string{
		"continue":                  "true",
		"max-connection-per-server": "16",
		"split":                     "16",
		"min-split-size":            "1M",
		"max-tries":                 "10",
		"retry-wait":                "3",
		"auto-file-renaming":        "true",
	}
	for k, v := range opts {
		options[k] = v
	}
	raw, err := c.call(ctx, "aria2.addUri", []string{uri}, options)
	if err != nil {
		return "", err
	}
	var gid string
	if err := json.Unmarshal(raw, &gid); err != nil {
		return "", err
	}
	return gid, nil
}

// Status is the subset of aria2 tellStatus we need for the UI.
type Status struct {
	GID             string `json:"gid"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	DownloadSpeed   string `json:"downloadSpeed"`
	ETA             string `json:"eta"`
	Status          string `json:"status"` // active, waiting, paused, complete, error, removed
	ErrorMessage    string `json:"errorMessage"`
}

// TellStatus fetches live progress for a gid.
func (c *aria2Client) TellStatus(ctx context.Context, gid string) (*Status, error) {
	raw, err := c.call(ctx, "aria2.tellStatus", gid)
	if err != nil {
		return nil, err
	}
	var st Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Remove cancels a download.
func (c *aria2Client) Remove(ctx context.Context, gid string) error {
	_, err := c.call(ctx, "aria2.remove", gid)
	return err
}

// Version checks daemon liveness.
func (c *aria2Client) Version(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "aria2.getVersion")
	if err != nil {
		return "", err
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// aria2Daemon owns the spawned aria2c RPC process.
type aria2Daemon struct {
	client *aria2Client
	cmd    *exec.Cmd
}

// StartAria2 launches aria2c in RPC mode and waits until it answers.
func StartAria2(binPath string, port int, secret string) (*aria2Daemon, error) {
	args := []string{
		"--enable-rpc",
		"--rpc-listen-port=" + strconv.Itoa(port),
		"--rpc-secret=" + secret,
		"--rpc-listen-all=false",
		"--max-connection-per-server=16",
		"--split=16",
		"--min-split-size=1M",
		"--max-tries=10",
		"--retry-wait=3",
		"--continue=true",
		"--auto-file-renaming=true",
		"--file-allocation=none",
		"--console-log-level=warn",
		"--summary-interval=0",
		"--quiet=true",
	}
	cmd := exec.Command(binPath, args...)
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start aria2c: %w", err)
	}

	d := &aria2Daemon{cmd: cmd, client: newAria2Client(port, secret)}

	// Poll until the RPC answers (or fail after ~10s).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := d.client.Version(ctx)
		cancel()
		if err == nil {
			return d, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = d.Stop()
	return nil, fmt.Errorf("aria2c did not answer on port %d", port)
}

// Stop shuts the daemon down.
func (d *aria2Daemon) Stop() error {
	if d == nil || d.cmd == nil || d.cmd.Process == nil {
		return nil
	}
	_ = d.cmd.Process.Kill()
	_, _ = d.cmd.Process.Wait()
	return nil
}
