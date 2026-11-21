package lambe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client reads Lambe's state API. It is read-only by construction: there is no refresh
// call, because POST /api/v1/refresh is a write path that needs an explicit owner decision.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient validates the descriptor host before the token is ever attached to a request.
func NewClient(d Descriptor, timeout time.Duration) (*Client, error) {
	if !loopback(d.Addr) {
		return nil, errors.New("refusing descriptor: addr host is not 127.0.0.1")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		base:  "http://" + d.Addr,
		token: d.BearerToken,
		http: &http.Client{
			Timeout: timeout,
			// A redirect is an error, never a hop: the token must not follow it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return errors.New("build request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		// Do's error embeds the URL, never the header; keep the message generic anyway.
		return fmt.Errorf("state API GET %s failed", path)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("state API GET %s: read body", path)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("state API GET %s: unexpected redirect (%d)", path, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		var e errorBody
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("state API GET %s: %d %s: %s", path, resp.StatusCode, e.Error, e.Message)
		}
		return fmt.Errorf("state API GET %s: status %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("state API GET %s: malformed response", path)
	}
	return nil
}

func (c *Client) Snapshot(ctx context.Context) (s Snapshot, err error) {
	err = c.get(ctx, "/api/v1/state", &s)
	return
}

func (c *Client) Queue(ctx context.Context) (q Queue, err error) {
	err = c.get(ctx, "/api/v1/queue", &q)
	return
}

func (c *Client) History(ctx context.Context) (h History, err error) {
	err = c.get(ctx, "/api/v1/history", &h)
	return
}

// View reads the snapshot and queue the status and TUI surfaces show.
func (c *Client) View(ctx context.Context) (View, error) {
	s, err := c.Snapshot(ctx)
	if err != nil {
		return View{}, err
	}
	q, err := c.Queue(ctx)
	if err != nil {
		return View{}, err
	}
	return View{Snapshot: s, Queue: q}, nil
}
