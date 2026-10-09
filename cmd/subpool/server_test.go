package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type shutdownTestServer struct {
	server  *http.Server
	address chan string
	entered chan struct{}
	stopped chan struct{}
	release func()
}

func newShutdownTestServer(t *testing.T) *shutdownTestServer {
	t.Helper()
	release := make(chan struct{})
	fixture := &shutdownTestServer{
		address: make(chan string, 1), entered: make(chan struct{}),
		stopped: make(chan struct{}), release: sync.OnceFunc(func() { close(release) }),
	}
	fixture.server = newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(fixture.entered)
		<-release
		_, _ = fmt.Fprint(w, "completed")
	}), time.Second)
	fixture.server.BaseContext = func(listener net.Listener) context.Context {
		fixture.address <- listener.Addr().String()
		return context.Background()
	}
	fixture.server.RegisterOnShutdown(func() { close(fixture.stopped) })
	t.Cleanup(func() {
		fixture.release()
		_ = fixture.server.Close()
	})
	return fixture
}

func waitForShutdownSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server event")
	}
}

func TestServeHTTPServersDrainsBothListenersBeforeReturning(t *testing.T) {
	api, console := newShutdownTestServer(t), newShutdownTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var shutdownCalls atomic.Int32
	returned := make(chan error, 1)
	go func() {
		returned <- serveHTTPServers(ctx, []serverBinding{
			{name: "API", server: api.server}, {name: "console", server: console.server},
		}, func() { shutdownCalls.Add(1) })
	}()
	responses := make(chan error, 2)
	for _, fixture := range []*shutdownTestServer{api, console} {
		var address string
		select {
		case address = <-fixture.address:
		case <-time.After(2 * time.Second):
			t.Fatal("listener did not start")
		}
		go func() {
			response, err := http.Get("http://" + address + "/slow")
			if err == nil {
				defer response.Body.Close()
				var body []byte
				body, err = io.ReadAll(response.Body)
				if err == nil && string(body) != "completed" {
					err = fmt.Errorf("incomplete response: %q", body)
				}
			}
			responses <- err
		}()
		waitForShutdownSignal(t, fixture.entered)
	}
	cancel()
	waitForShutdownSignal(t, api.stopped)
	waitForShutdownSignal(t, console.stopped)
	select {
	case err := <-returned:
		t.Fatalf("returned while requests were active: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	api.release()
	console.release()
	for range 2 {
		select {
		case err := <-responses:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("request did not complete")
		}
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("servers did not finish shutdown")
	}
	if calls := shutdownCalls.Load(); calls != 1 {
		t.Fatalf("shutdown callback called %d times", calls)
	}
}
