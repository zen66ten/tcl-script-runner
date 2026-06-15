package becs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

// passwordRe masks password values so credentials never reach the API monitor.
var passwordRe = regexp.MustCompile(`("password"\s*:\s*")(?:\\.|[^"\\])*(")`)

func redact(s string) string {
	return passwordRe.ReplaceAllString(s, `${1}***${2}`)
}

// Client talks to a single BECS JSON-RPC endpoint (spec §3.3).
// Use NewClient to construct. After Login the SessionID field is set and
// included automatically in every subsequent call.
type Client struct {
	endpoint  string // e.g. "http://127.0.0.1:4499/"
	http      *http.Client
	SessionID string
	nextID    int
}

// NewClient creates a Client for the given endpoint. transport may be nil
// (uses http.DefaultTransport) or a custom transport — WireGuard tunnels pass
// one with tnet.DialContext so only BECS traffic routes through the VPN.
func NewClient(endpoint string, transport http.RoundTripper) *Client {
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
	}
	return &Client{endpoint: endpoint, http: httpClient}
}

// Login calls sessionLogin and stores the returned session ID.
func (c *Client) Login(ctx context.Context, username, password string) error {
	params := loginParams{
		Header:   header{SessionID: ""},
		Username: username,
		Password: password,
	}
	var result loginResult
	if err := c.call(ctx, "sessionLogin", params, &result); err != nil {
		return fmt.Errorf("sessionLogin: %w", err)
	}
	if result.Err != 0 {
		return fmt.Errorf("sessionLogin: BECS error %d: %s", result.Err, result.ErrTxt)
	}
	c.SessionID = result.SessionID
	return nil
}

// Logout calls sessionLogout and clears the session ID.
func (c *Client) Logout(ctx context.Context) error {
	params := logoutParams{Header: header{SessionID: c.SessionID}}
	var result logoutResult
	if err := c.call(ctx, "sessionLogout", params, &result); err != nil {
		return fmt.Errorf("sessionLogout: %w", err)
	}
	if result.Err != 0 {
		return fmt.Errorf("sessionLogout: BECS error %d: %s", result.Err, result.ErrTxt)
	}
	c.SessionID = ""
	return nil
}

// call sends a JSON-RPC 2.0 request and unmarshals the result into out.
// It checks the JSON-RPC error object first, then the BECS-level err field is
// checked by each caller (Login, Logout, etc.) after call returns.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.nextID++
	req := request{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      c.nextID,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	slog.Info("rpc →", "method", method, "id", c.nextID, "body", string(body))
	Log.Record(LogEntry{Dir: "→", Method: method, ID: c.nextID, Endpoint: c.endpoint, Body: redact(string(body))})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		slog.Info("rpc ✗", "method", method, "err", err)
		Log.Record(LogEntry{Dir: "✗", Method: method, ID: c.nextID, Endpoint: c.endpoint, Body: "http error: " + err.Error()})
		return fmt.Errorf("http: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		slog.Info("rpc ✗", "method", method, "http_status", httpResp.StatusCode)
		Log.Record(LogEntry{Dir: "✗", Method: method, ID: c.nextID, Endpoint: c.endpoint, Body: fmt.Sprintf("http status %d", httpResp.StatusCode)})
		return fmt.Errorf("http status %d", httpResp.StatusCode)
	}

	var rpcResp response
	if err := json.NewDecoder(httpResp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if rpcResp.Error != nil {
		slog.Info("rpc ✗", "method", method, "rpc_err", rpcResp.Error.Code, "msg", rpcResp.Error.Message)
		Log.Record(LogEntry{Dir: "✗", Method: method, ID: c.nextID, Endpoint: c.endpoint, Body: fmt.Sprintf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)})
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	slog.Info("rpc ←", "method", method, "result", string(rpcResp.Result))
	Log.Record(LogEntry{Dir: "←", Method: method, ID: c.nextID, Endpoint: c.endpoint, Body: redact(string(rpcResp.Result))})

	if out != nil {
		if err := json.Unmarshal(rpcResp.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}
