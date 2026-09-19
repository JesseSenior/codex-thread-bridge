package rpc_test

import (
	"context"
	"errors"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/rpc"
	"github.com/JesseSenior/codex-thread-bridge/internal/testserver"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMultiplexedNotifications(t *testing.T) {
	f := testserver.Start(t)
	c := rpc.New(f.Socket)
	defer c.Close()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := c.Call(context.Background(), "thread/goal/get", j.Object{"threadId": "unused"})
			if e != nil || !j.Equal(v, j.Object{"goal": nil}) {
				t.Errorf("%v %v", v, e)
			}
		}()
	}
	wg.Wait()
	f.Edit(func(f *testserver.Fake) {
		if len(f.Extensions) != 1 || f.Extensions[0] != "" {
			t.Error(f.Extensions)
		}
	})
}
func TestErrorsAndReconnectOnlyForNewRequests(t *testing.T) {
	f := testserver.Start(t)
	c := rpc.New(f.Socket)
	defer c.Close()
	f.Edit(func(f *testserver.Fake) {
		f.Reject["thread/goal/get"] = j.Object{"code": -32601, "message": "unsupported"}
	})
	_, e := c.Call(context.Background(), "thread/goal/get", j.Object{})
	var api *rpc.Error
	if !errors.As(e, &api) || !strings.Contains(e.Error(), "unsupported") {
		t.Fatal(e)
	}
	f.Edit(func(f *testserver.Fake) { clear(f.Reject); f.Drop = "thread/goal/get" })
	_, e = c.Call(context.Background(), "thread/goal/get", j.Object{})
	var transport *rpc.TransportError
	if !errors.As(e, &transport) {
		t.Fatal(e)
	}
	if f.Count("thread/goal/get") != 2 {
		t.Fatal("replayed")
	}
	f.Edit(func(f *testserver.Fake) { f.Drop = "" })
	if _, e = c.Call(context.Background(), "thread/goal/get", j.Object{}); e != nil {
		t.Fatal(e)
	}
	if f.Count("thread/goal/get") != 3 {
		t.Fatal("wrong request count")
	}
}
func TestTimeoutNoRetry(t *testing.T) {
	f := testserver.Start(t)
	c := rpc.New(f.Socket)
	defer c.Close()
	c.Timeout = 50 * time.Millisecond
	f.Edit(func(f *testserver.Fake) { f.Pause = "thread/goal/get" })
	defer close(f.Release)
	_, e := c.Call(context.Background(), "thread/goal/get", j.Object{})
	if e == nil || !strings.Contains(e.Error(), "do not resend") {
		t.Fatal(e)
	}
	if f.Count("thread/goal/get") != 1 {
		t.Fatal("replayed")
	}
}
func TestUnansweredInteractions(t *testing.T) {
	for _, method := range []string{"item/tool/call", "item/tool/requestUserInput", "item/commandExecution/requestApproval"} {
		for _, resolution := range []string{"serverRequest/resolved", "turn/completed"} {
			t.Run(method+"/"+resolution, func(t *testing.T) {
				f := testserver.Start(t)
				id := f.Add(t.TempDir(), nil)
				f.Edit(func(f *testserver.Fake) { f.PendingMethod = method; f.Resolution = resolution })
				c := rpc.New(f.Socket)
				defer c.Close()
				p := j.Object{"threadId": id}
				if _, e := c.Call(context.Background(), "thread/resume", p); e != nil {
					t.Fatal(e)
				}
				if len(c.Interactions()) != 1 || c.Interactions()[0]["method"] != method {
					t.Fatal(c.Interactions())
				}
				if _, e := c.Call(context.Background(), "thread/read", p); e != nil {
					t.Fatal(e)
				}
				if len(c.Interactions()) != 0 {
					t.Fatal(c.Interactions())
				}
				if _, e := c.Call(context.Background(), "thread/resume", p); e != nil {
					t.Fatal(e)
				}
				c.Close()
				if len(c.Interactions()) != 0 {
					t.Fatal("not cleared")
				}
				f.Edit(func(f *testserver.Fake) {
					if len(f.Responses) != 0 {
						t.Error("answered Desktop request")
					}
				})
			})
		}
	}
}
