package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/datadir"
)

func TestClientRetriesAcrossUnixSocketSwap(t *testing.T) {
	sock := datadir.DeterministicSocketPath(filepath.Join(t.TempDir(), "capability.sock"), "sumi-cli-t", t.TempDir())
	_ = os.Remove(sock)
	var hits atomic.Int32

	// Start the Unix listener shortly after the client begins dialing, simulating a
	// ~50ms Computer daemon hot-swap window.
	started := make(chan struct{})
	var srv *http.Server
	go func() {
		time.Sleep(40 * time.Millisecond)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			close(started)
			return
		}
		srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})}
		close(started)
		_ = srv.Serve(ln)
	}()
	defer func() {
		<-started
		if srv != nil {
			_ = srv.Close()
		}
		_ = os.Remove(sock)
	}()

	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	c := &Client{
		socketPath:  sock,
		driverToken: "tok",
		httpClient:  &http.Client{Transport: tr, Timeout: 2 * time.Second},
		retryDelays: []time.Duration{20 * time.Millisecond, 30 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond},
	}

	status, _, err := c.DoWithKey(http.MethodPost, "/conversations/c1/messages", map[string]string{"body_markdown": "hi"}, "idem-swap-1")
	if err != nil || status != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("expected retry to succeed after socket appears: status=%d hits=%d err=%v", status, hits.Load(), err)
	}
}
