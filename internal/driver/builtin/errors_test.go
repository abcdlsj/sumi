package builtin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
)

func failureCode(t *testing.T, baseURL string) (string, error) {
	t.Helper()
	h, err := New(Config{BaseURL: baseURL, Model: "m", APIKey: "sk-classify"}, nil)
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	_, err = h.RunTurn(context.Background(), driver.TurnInput{UserPrompt: "hi", WorkDir: t.TempDir(), MemoryDir: t.TempDir()})
	var f *driver.Failure
	if !errors.As(err, &f) {
		t.Fatalf("expected driver.Failure, got %v", err)
	}
	return f.Code, err
}

func statusServer(t *testing.T, status int) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream said no","type":"x"}}`))
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/v1"
}

func TestModelErrorClassification(t *testing.T) {
	if code, err := failureCode(t, statusServer(t, 401)); code != domain.AgentErrModelAuthFailed {
		t.Fatalf("401 -> %s (%v)", code, err)
	}
	if code, _ := failureCode(t, statusServer(t, 403)); code != domain.AgentErrModelAuthFailed {
		t.Fatalf("403 -> %s", code)
	}
	if code, _ := failureCode(t, statusServer(t, 400)); code != domain.AgentErrTurnFailed {
		t.Fatalf("400 -> %s", code)
	}
	if code, _ := failureCode(t, statusServer(t, 503)); code != domain.AgentErrModelUnreachable {
		t.Fatalf("503 -> %s", code)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL + "/v1"
	closed.Close()
	code, err := failureCode(t, url)
	if code != domain.AgentErrModelUnreachable {
		t.Fatalf("connection refused -> %s (%v)", code, err)
	}
	if strings.Contains(err.Error(), "sk-classify") {
		t.Fatalf("error leaked api key")
	}
}
