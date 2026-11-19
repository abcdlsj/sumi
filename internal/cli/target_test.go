package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// rewriteTransport sends the Client's unix-socket style requests to a local test server.
type rewriteTransport struct{ base *url.URL }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.URL.Scheme = rt.base.Scheme
	clone.URL.Host = rt.base.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test url: %v", err)
	}
	return &Client{driverToken: "token", httpClient: &http.Client{Transport: rewriteTransport{base: base}}}
}

func writeFakeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode fake response: %v", err)
	}
}

func fakeServer(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations":
			writeFakeJSON(t, w, map[string]any{"conversations": []map[string]any{
				{"id": "c-general", "kind": "public", "slug": "general"},
				{"id": "c-ops", "kind": "private", "slug": "ops"},
				{"id": "c-dm", "kind": "dm", "peer_member_id": "m-lin", "peer_display_name": "Lin"},
			}})
		case "/tasks":
			if got := r.URL.Query().Get("status"); got != "all" {
				t.Errorf("task lookup must list every status, got %q", got)
			}
			writeFakeJSON(t, w, map[string]any{"tasks": []map[string]any{
				{"id": "t-14", "number": 14, "conversation_id": "c-task-14"},
				{"id": "t-15", "number": 15, "conversation_id": ""},
			}})
		case "/tasks/t-15/conversation":
			if r.Method != http.MethodPost {
				t.Errorf("materializing a task conversation must POST, got %s", r.Method)
			}
			writeFakeJSON(t, w, map[string]any{"task": map[string]any{"id": "t-15", "number": 15, "conversation_id": "c-task-15"}})
		default:
			w.WriteHeader(http.StatusNotFound)
			writeFakeJSON(t, w, map[string]any{"error": map[string]any{"code": "not_found", "message": "unexpected path " + r.URL.Path}})
		}
	}
}

func TestResolveTargetForms(t *testing.T) {
	c := testClient(t, fakeServer(t))
	cases := []struct{ target, want string }{
		{"#general", "c-general"},
		{"#General", "c-general"},
		{"#ops", "c-ops"},
		{"dm:@Lin", "c-dm"},
		{"dm:Lin", "c-dm"},
		{"dm:m-lin", "c-dm"},
		{"thread:c-thread", "c-thread"},
		{"task:#14", "c-task-14"},
		{"task:#15", "c-task-15"},
		{"c-bare", "c-bare"},
	}
	for _, tc := range cases {
		got, e := resolveTarget(c, tc.target)
		if e != nil {
			t.Fatalf("resolve %q: %v", tc.target, e)
		}
		if got != tc.want {
			t.Fatalf("resolve %q = %q, want %q", tc.target, got, tc.want)
		}
	}
}

func TestChannelAndSpaceCommandsUseServerRoutes(t *testing.T) {
	var seen []string
	serveSocket(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/conversations":
			writeFakeJSON(t, w, map[string]any{"conversations": []map[string]any{{"id": "c-ops", "kind": "private", "slug": "ops"}}})
		case "/members":
			writeFakeJSON(t, w, map[string]any{"members": []map[string]any{}})
		case "/tasks/t-1":
			writeFakeJSON(t, w, map[string]any{"task": map[string]any{"id": "t-1"}})
		case "/conversations/c-ops/members":
			if r.Method == http.MethodPost {
				var body struct {
					MemberID string `json:"member_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.MemberID != "m-1" {
					t.Errorf("invite body = %+v err=%v", body, err)
				}
			}
			writeFakeJSON(t, w, map[string]any{"members": []map[string]any{}, "ok": true})
		case "/conversations/c-ops/members/m-1":
			writeFakeJSON(t, w, map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusNotFound)
			writeFakeJSON(t, w, map[string]any{"error": map[string]any{"code": "not_found"}})
		}
	})

	for _, args := range [][]string{
		{"space", "members"},
		{"task", "show", "t-1"},
		{"channel", "members", "#ops"},
		{"channel", "invite", "#ops", "m-1"},
		{"channel", "remove", "#ops", "m-1"},
	} {
		if env, _ := runCLI(t, args...); !env.OK {
			t.Fatalf("%v failed: %+v", args, env.Error)
		}
	}

	want := []string{
		"GET /members",
		"GET /tasks/t-1",
		"GET /conversations",
		"GET /conversations/c-ops/members",
		"GET /conversations",
		"POST /conversations/c-ops/members",
		"GET /conversations",
		"DELETE /conversations/c-ops/members/m-1",
	}
	if len(seen) != len(want) {
		t.Fatalf("requests = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("request %d = %q, want %q (all: %v)", i, seen[i], want[i], seen)
		}
	}
}

func TestResolveTargetErrors(t *testing.T) {
	c := testClient(t, fakeServer(t))
	cases := []struct{ target, code string }{
		{"", codeInvalidArgument},
		{"#", codeInvalidArgument},
		{"dm:", codeInvalidArgument},
		{"dm:@", codeInvalidArgument},
		{"thread:", codeInvalidArgument},
		{"task:#", codeInvalidArgument},
		{"task:#abc", codeInvalidArgument},
		{"#missing", "not_found"},
		{"dm:@Nobody", "not_found"},
		{"task:#99", "not_found"},
	}
	for _, tc := range cases {
		if _, e := resolveTarget(c, tc.target); e == nil || e.Code != tc.code {
			t.Fatalf("resolve %q error = %+v, want code %s", tc.target, e, tc.code)
		}
	}
}
