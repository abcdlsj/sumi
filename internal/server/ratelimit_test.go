package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPLimiterWindowsPerPeer(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newIPLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	a := httptest.NewRequest("POST", "/", nil)
	a.RemoteAddr = "10.0.0.1:5000"
	b := httptest.NewRequest("POST", "/", nil)
	b.RemoteAddr = "10.0.0.2:5000"
	// Forwarding headers are client-controlled and must not reset the bucket.
	a.Header.Set("X-Forwarded-For", "1.2.3.4")

	if !l.allow(a) || !l.allow(a) {
		t.Fatal("first two requests must pass")
	}
	if l.allow(a) {
		t.Fatal("third request in the window must be limited")
	}
	if !l.allow(b) {
		t.Fatal("another peer has its own window")
	}
	now = now.Add(time.Minute)
	if !l.allow(a) {
		t.Fatal("a new window must allow again")
	}
}
