package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/store"
)

func pairingTestServer(t *testing.T) (*app.Service, *store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := app.NewService(st, app.NewEventHub(), nil)
	ts := httptest.NewServer(server.New(svc, server.Config{}, nil).Handler())
	t.Cleanup(ts.Close)
	return svc, st, ts.URL
}

func parsePairURL(t *testing.T, base, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || base+"/" != u.Scheme+"://"+u.Host+u.Path {
		t.Fatalf("unexpected pair url base: %v", err)
	}
	q := u.Query()
	if q.Get("pair") == "" || q.Get("code") == "" {
		t.Fatal("pair url missing pair or code")
	}
	return q.Get("pair"), q.Get("code")
}

func TestPairerConfirmsAndRenewsExpiredPairing(t *testing.T) {
	svc, st, base := pairingTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	auth, err := svc.Register(ctx, "lisi@sumi.dev", "password123", "Lisi")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_, _, err = svc.CreateSpace(ctx, auth.User, "studio", "Studio")
	if err != nil {
		t.Fatalf("create space: %v", err)
	}

	urls := make(chan string, 4)
	p := &pairer{
		client:   newPairingClient(base),
		hostname: "mac.local",
		osName:   "macos",
		interval: 20 * time.Millisecond,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		onURL:    func(u string) { urls <- u },
	}
	type result struct {
		secret string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		s, err := p.run(ctx, nil)
		done <- result{s, err}
	}()

	firstID, _ := parsePairURL(t, base, <-urls)
	if err := st.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE computer_pairings SET expires_at = ? WHERE id = ?`,
			store.FormatTime(time.Now().UTC().Add(-time.Second)), firstID)
		return err
	}); err != nil {
		t.Fatalf("expire pairing: %v", err)
	}

	secondID, code := parsePairURL(t, base, <-urls)
	if secondID == firstID {
		t.Fatal("expired pairing must be replaced")
	}
	comp, err := svc.ConfirmPairing(ctx, auth.User, secondID, code, "")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("run: %v", res.err)
	}
	authed, err := svc.AuthenticateComputer(ctx, res.secret)
	if err != nil || authed.ID != comp.ID {
		t.Fatalf("returned secret must authenticate as the confirmed computer: err=%v", err)
	}
	select {
	case <-urls:
		t.Fatal("no URL updates after confirmation")
	default:
	}
}

func TestPairerStopsOnCancel(t *testing.T) {
	_, _, base := pairingTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	p := newPairer(base, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.interval = 10 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := p.run(ctx, nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pairer did not stop after cancel")
	}
}

func TestPairingClientRejectsWrongSecret(t *testing.T) {
	_, _, base := pairingTestServer(t)
	c := newPairingClient(base)
	tk, err := c.create(context.Background(), "mac.local", "macos")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := c.poll(context.Background(), tk)
	if err != nil || res.Status != "pending" {
		t.Fatalf("poll = %+v err=%v", res, err)
	}
	tk.Secret = "wrong"
	if _, err := c.poll(context.Background(), tk); err == nil {
		t.Fatal("wrong secret must fail")
	}
}

func TestWriteConfigAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "desktop.json")
	want := Config{ServerURL: "http://127.0.0.1:8087", ComputerToken: "tok", DataDir: "/tmp/sumi"}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"server_url":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigAtomic(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	var got Config
	if err := json.Unmarshal(raw, &got); err != nil || got != want {
		t.Fatal("config round-trip mismatch")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %d entries", len(entries))
	}
}

func TestPairURLEscapesParams(t *testing.T) {
	got := pairURL("http://127.0.0.1:8087", pairingTicket{ID: "a b", Code: "ABC234"})
	if got != "http://127.0.0.1:8087/?code=ABC234&pair=a+b&shell=desktop" {
		t.Fatalf("pair url = %s", got)
	}
}
