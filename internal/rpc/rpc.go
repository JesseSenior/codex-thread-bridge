// Package rpc connects to one existing App Server without replaying requests.
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/version"
	"github.com/coder/websocket"
)

type Error struct {
	Method string
	Data   j.Object
}

func (e *Error) Error() string { b, _ := json.Marshal(e.Data); return e.Method + ": " + string(b) }

type TransportError struct {
	Message         string
	MayHaveBeenSent bool
}

func (e *TransportError) Error() string { return e.Message }

type reply struct {
	value j.Object
	err   error
}
type connection struct {
	ws      *websocket.Conn
	done    chan struct{}
	pending map[string]chan reply
}
type Client struct {
	Socket       string
	Timeout      time.Duration
	connectMu    sync.Mutex
	mu           sync.Mutex
	conn         *connection
	counter      uint64
	info         j.Object
	interactions map[string]j.Object
}

func New(socket string) *Client {
	return &Client{Socket: socket, Timeout: 20 * time.Second, interactions: map[string]j.Object{}}
}
func (c *Client) Info() j.Object { c.mu.Lock(); defer c.mu.Unlock(); return c.info }
func (c *Client) Interactions() []j.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]j.Object, 0, len(c.interactions))
	for _, v := range c.interactions {
		out = append(out, v)
	}
	return out
}
func (c *Client) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	c.mu.Lock()
	old := c.conn
	c.mu.Unlock()
	if old != nil {
		select {
		case <-old.done:
		default:
			return nil
		}
	}
	c.closeConnection()
	dialCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	}}
	defer transport.CloseIdleConnections()
	ws, _, err := websocket.Dial(dialCtx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return err
	}
	ws.SetReadLimit(16 * 1024 * 1024)
	conn := &connection{ws: ws, done: make(chan struct{}), pending: map[string]chan reply{}}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	go c.receive(conn)
	info, err := c.request(ctx, conn, "initialize", j.Object{"clientInfo": j.Object{"name": "codex_thread_bridge", "version": version.Version}, "capabilities": j.Object{"experimentalApi": true}})
	if err == nil {
		err = c.write(dialCtx, conn, j.Object{"method": "initialized", "params": j.Object{}})
	}
	if err != nil {
		c.closeConnection()
		return err
	}
	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return nil
}
func (c *Client) receive(conn *connection) {
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		close(conn.done)
		for _, ch := range conn.pending {
			select {
			case ch <- reply{err: &TransportError{"App Server disconnected", true}}:
			default:
			}
		}
	}()
	for {
		_, raw, err := conn.ws.Read(context.Background())
		if err != nil {
			return
		}
		var msg j.Object
		if j.Decode(raw, &msg) != nil {
			conn.ws.CloseNow()
			return
		}
		c.mu.Lock()
		if method, ok := msg["method"].(string); ok {
			p := j.Map(msg["params"])
			if id, exists := msg["id"]; exists {
				c.interactions[idKey(id)] = msg
			} else if method == "serverRequest/resolved" {
				delete(c.interactions, idKey(p["requestId"]))
			} else if method == "turn/completed" {
				for id, req := range c.interactions {
					rp := j.Map(req["params"])
					if j.Equal(rp["threadId"], p["threadId"]) && j.Equal(rp["turnId"], j.Map(p["turn"])["id"]) {
						delete(c.interactions, id)
					}
				}
			}
		} else if ch := conn.pending[idKey(msg["id"])]; ch != nil {
			select {
			case ch <- reply{value: msg}:
			default:
			}
		}
		c.mu.Unlock()
	}
}
func idKey(v any) string { b, _ := json.Marshal(v); return string(b) }
func (c *Client) write(ctx context.Context, conn *connection, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.ws.Write(ctx, websocket.MessageText, b)
}
func (c *Client) request(ctx context.Context, conn *connection, method string, params j.Object) (j.Object, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	c.mu.Lock()
	c.counter++
	id := c.counter
	key := fmt.Sprint(id)
	ch := make(chan reply, 1)
	conn.pending[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(conn.pending, key); c.mu.Unlock() }()
	failure := func() (j.Object, error) {
		return nil, &TransportError{method + ": response unavailable; do not resend", true}
	}
	if err := c.write(ctx, conn, j.Object{"id": id, "method": method, "params": params}); err != nil {
		return failure()
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if e, ok := r.value["error"]; ok {
			return nil, &Error{method, j.Map(e)}
		}
		if result, ok := r.value["result"]; ok {
			m, ok := result.(map[string]any)
			if ok {
				return m, nil
			}
		}
		return nil, &TransportError{method + ": invalid response; outcome unknown", true}
	case <-ctx.Done():
		return failure()
	}
}
func (c *Client) Call(ctx context.Context, method string, params j.Object) (j.Object, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, &TransportError{fmt.Sprintf("App Server unavailable at %s: %v; %s was not sent", c.Socket, err, method), false}
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, &TransportError{"App Server is not connected", false}
	}
	return c.request(ctx, conn, method, params)
}
func (c *Client) closeConnection() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn != nil {
		done := make(chan struct{})
		go func() { _ = conn.ws.Close(websocket.StatusNormalClosure, ""); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = conn.ws.CloseNow()
			<-done
		}
		<-conn.done
	}
	c.mu.Lock()
	clear(c.interactions)
	c.mu.Unlock()
}
func (c *Client) Close() { c.connectMu.Lock(); defer c.connectMu.Unlock(); c.closeConnection() }
