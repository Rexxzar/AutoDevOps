package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base string
	tok  string
	http *http.Client
}

func newClient(cfg *Config) *Client {
	return &Client{
		base: strings.TrimRight(cfg.ServerURL, "/"),
		tok:  cfg.Token,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) req(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	return c.http.Do(req)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	resp, err := c.req(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	resp, err := c.req(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) delete(ctx context.Context, path string) error {
	resp, err := c.req(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return apiError(resp)
	}
	return nil
}

type SSEEvent struct {
	Type string
	Data string
}

func (c *Client) streamSSE(ctx context.Context, path string, fn func(SSEEvent)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	cl := &http.Client{} // no timeout — long-lived
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var ev SSEEvent
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			ev.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			ev.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			if ev.Type != "" || ev.Data != "" {
				fn(ev)
			}
			ev = SSEEvent{}
		}
	}
	return scanner.Err()
}

func apiError(resp *http.Response) error {
	var e struct{ Error string `json:"error"` }
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &e); err == nil && e.Error != "" {
		return fmt.Errorf("server: %s", e.Error)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
