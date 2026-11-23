package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func marshalJSONLLines(lines []map[string]any) []byte {
	var buf bytes.Buffer
	for _, obj := range lines {
		raw, _ := json.Marshal(obj)
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func writeFakeCompactedV3Session(path, sessionID string, bloatBytes int) {
	pad := strings.Repeat("x", bloatBytes)
	lines := []map[string]any{
		{"type": "session", "version": 3, "id": sessionID, "timestamp": "2026-11-23T00:00:00.000Z", "cwd": "/work"},
		{"type": "model_change", "id": "e1", "parentId": nil, "provider": "p1", "modelId": "m1"},
		{"type": "message", "id": "e2", "parentId": "e1", "message": map[string]any{"role": "user", "content": pad}},
		{"type": "message", "id": "e3", "parentId": "e2", "message": map[string]any{"role": "assistant", "api": "openai", "content": pad}},
		{"type": "message", "id": "e4", "parentId": "e3", "message": map[string]any{"role": "user", "content": "recent user msg"}},
		{"type": "message", "id": "e5", "parentId": "e4", "message": map[string]any{"role": "assistant", "api": "openai", "content": "recent reply"}},
		{"type": "compaction", "id": "e6", "parentId": "e5", "firstKeptEntryId": "e4", "summary": "summary of older work", "tokensBefore": 200000},
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, marshalJSONLLines(lines), 0o600)
}

func TestBuildSessionSeedKeepsStateAndKeptSegment(t *testing.T) {
	oldFile := filepath.Join(t.TempDir(), "2026-11-23T00-00-00-000Z_sess-abc.jsonl")
	raw := marshalJSONLLines([]map[string]any{
		{"type": "session", "version": 3, "id": "sess-abc", "timestamp": "2026-11-23T00:00:00.000Z", "cwd": "/repo"},
		{"type": "model_change", "id": "s1", "parentId": nil, "provider": "p1", "modelId": "old-model"},
		{"type": "thinking_level_change", "id": "s2", "parentId": "s1", "thinkingLevel": "low"},
		{"type": "custom", "id": "s3", "parentId": "s2", "customType": "pi-virtual-model-state", "data": map[string]any{"provider": "ak", "modelId": "v1", "route": "a"}},
		{"type": "message", "id": "m1", "parentId": "s3", "message": map[string]any{"role": "user", "content": strings.Repeat("old ", 500)}},
		{"type": "usage", "id": "u1", "parentId": "m1", "tokens": 50000},
		{"type": "model_change", "id": "s4", "parentId": "u1", "provider": "p2", "modelId": "new-model"},
		{"type": "custom", "id": "s5", "parentId": "s4", "customType": "pi-virtual-model-state", "data": map[string]any{"provider": "ak", "modelId": "v1", "route": "b"}},
		// Kept segment starts at k1.
		{"type": "message", "id": "k1", "parentId": "s5", "message": map[string]any{"role": "user", "content": "kept question"}},
		{"type": "message", "id": "k2", "parentId": "k1", "message": map[string]any{"role": "assistant", "api": "anthropic", "content": "kept answer"}},
		{"type": "compaction", "id": "c1", "parentId": "k2", "firstKeptEntryId": "k1", "summary": "condensed", "tokensBefore": 180000},
	})

	now := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	res := buildSessionSeed(oldFile, raw, now)
	if !res.rotatable {
		t.Fatalf("expected rotatable seed, got refusal=%q", res.refusalReason)
	}
	if len(res.content) >= len(raw) {
		t.Fatalf("seed must be smaller than original: after=%d before=%d", len(res.content), len(raw))
	}
	if filepath.Base(res.seedFile) != "2026-11-23T01-00-00-000Z_sess-abc.jsonl" {
		t.Fatalf("unexpected seed filename: %s", res.seedFile)
	}

	lines := strings.Split(strings.TrimSpace(string(res.content)), "\n")
	// Header + 3 state entries (s2 thinking, s4 model_change, s5 virtual-model-state) + 3 kept segment entries (k1, k2, c1) = 7 lines.
	if len(lines) != 7 {
		t.Fatalf("expected 7 lines in seed, got %d:\n%s", len(lines), string(res.content))
	}

	var hdr map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &hdr); err != nil {
		t.Fatalf("unmarshal seed header: %v", err)
	}
	if hdr["id"] != "sess-abc" || hdr["version"].(float64) != 3 || hdr["parentSession"] != oldFile+".rotated" {
		t.Fatalf("unexpected seed header: %+v", hdr)
	}

	wantIDs := []string{"s2", "s4", "s5", "k1", "k2", "c1"}
	var prevID any = nil
	for i, line := range lines[1:] {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshal entry %d: %v", i, err)
		}
		if entry["id"] != wantIDs[i] {
			t.Fatalf("entry %d id = %v, want %s", i, entry["id"], wantIDs[i])
		}
		if entry["parentId"] != prevID {
			t.Fatalf("entry %s parentId = %v, want %v", wantIDs[i], entry["parentId"], prevID)
		}
		prevID = entry["id"]
	}
}

func TestBuildSessionSeedSafetyRefusals(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "2026-11-23T00-00-00-000Z_sess-1.jsonl")
	now := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		now        time.Time
		lines      []map[string]any
		wantReason string
	}{
		{
			name: "unsupported_version",
			now:  now,
			lines: []map[string]any{
				{"type": "session", "version": 2, "id": "sess-1", "cwd": "/w"},
				{"type": "message", "id": "e1", "parentId": nil},
			},
			wantReason: "unsupported_session_version",
		},
		{
			name: "seed_name_not_newer",
			now:  time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
			lines: []map[string]any{
				{"type": "session", "version": 3, "id": "sess-1", "cwd": "/w"},
				{"type": "message", "id": "e1", "parentId": nil},
			},
			wantReason: "seed_name_not_newer",
		},
		{
			name: "unknown_entry_type",
			now:  now,
			lines: []map[string]any{
				{"type": "session", "version": 3, "id": "sess-1", "cwd": "/w"},
				{"type": "future_widget", "id": "e1", "parentId": nil},
			},
			wantReason: "unknown_entry_type",
		},
		{
			name: "no_compaction",
			now:  now,
			lines: []map[string]any{
				{"type": "session", "version": 3, "id": "sess-1", "cwd": "/w"},
				{"type": "message", "id": "e1", "parentId": nil, "message": map[string]any{"role": "assistant", "api": "openai"}},
			},
			wantReason: "no_compaction",
		},
		{
			name: "first_kept_not_on_branch",
			now:  now,
			lines: []map[string]any{
				{"type": "session", "version": 3, "id": "sess-1", "cwd": "/w"},
				{"type": "message", "id": "e1", "parentId": nil, "message": map[string]any{"role": "assistant", "api": "openai"}},
				{"type": "compaction", "id": "c1", "parentId": "e1", "firstKeptEntryId": "missing"},
			},
			wantReason: "first_kept_entry_not_on_branch",
		},
		{
			name: "no_assistant_response_in_kept_segment",
			now:  now,
			lines: []map[string]any{
				{"type": "session", "version": 3, "id": "sess-1", "cwd": "/w"},
				{"type": "message", "id": "e1", "parentId": nil, "message": map[string]any{"role": "assistant", "api": "pi-virtual"}},
				{"type": "compaction", "id": "c1", "parentId": "e1", "firstKeptEntryId": "e1"},
			},
			wantReason: "no_response_in_kept_segment",
		},
		{
			name: "unknown_custom_type_before_segment",
			now:  now,
			lines: []map[string]any{
				{"type": "session", "version": 3, "id": "sess-1", "cwd": "/w"},
				{"type": "custom", "id": "x1", "parentId": nil, "customType": "codemode-store", "data": map[string]any{"k": "v"}},
				{"type": "message", "id": "e1", "parentId": "x1", "message": map[string]any{"role": "assistant", "api": "openai"}},
				{"type": "compaction", "id": "c1", "parentId": "e1", "firstKeptEntryId": "e1"},
			},
			wantReason: "unknown_custom_type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := buildSessionSeed(oldFile, marshalJSONLLines(tc.lines), tc.now)
			if res.rotatable || res.refusalReason != tc.wantReason {
				t.Fatalf("got rotatable=%v reason=%q, want refusal %q", res.rotatable, res.refusalReason, tc.wantReason)
			}
		})
	}
}

func TestLiveDriverRotatesSessionFileAfterCompaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	now := time.Date(2026, 11, 23, 2, 0, 0, 0, time.UTC)
	d, in, logPath := newFakeDriver(t, "rotate_after_compact", Config{
		Fingerprint:          "fp",
		SoftCompactionTokens: 150_000,
		SessionRotateBytes:   1024,
		Now:                  func() time.Time { return now },
	})
	defer d.Close()

	// Turn 1: exceeds soft limit -> compacts -> fake Pi writes >4KB v3 JSONL -> driver rotates to lean seed.
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	origFile := filepath.Join(d.cfg.SessionDir, "2026-11-23T00-00-00-000Z_sess-rot.jsonl")
	if _, err := os.Stat(origFile + ".rotated"); err != nil {
		t.Fatalf("expected original session file renamed to .rotated, err=%v", err)
	}
	if _, err := os.Stat(origFile); !os.IsNotExist(err) {
		t.Fatalf("original unrotated path must no longer exist, err=%v", err)
	}

	wantSeed := filepath.Join(d.cfg.SessionDir, "2026-11-23T02-00-00-000Z_sess-rot.jsonl")
	seedInfo, err := os.Stat(wantSeed)
	if err != nil {
		t.Fatalf("expected rotated seed file %s, err=%v", wantSeed, err)
	}
	rotInfo, _ := os.Stat(origFile + ".rotated")
	if seedInfo.Size() >= rotInfo.Size() {
		t.Fatalf("rotated seed (%d bytes) must be smaller than original (%d bytes)", seedInfo.Size(), rotInfo.Size())
	}

	// Saved session pointer must reference the new lean seed file.
	rawMeta, err := os.ReadFile(d.cfg.SessionFile)
	if err != nil {
		t.Fatalf("read saved session meta: %v", err)
	}
	var saved savedSession
	if err := json.Unmarshal(rawMeta, &saved); err != nil || saved.SessionFile != wantSeed {
		t.Fatalf("saved session must point to rotated seed %s, got %+v err=%v", wantSeed, saved, err)
	}

	// Turn 2: resumes the rotated seed seamlessly without re-injecting StartupContext.
	_ = os.Remove(logPath)
	now = now.Add(time.Minute)
	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	joined := strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "OPEN:resume:"+filepath.Base(wantSeed)) {
		t.Fatalf("turn 2 must resume rotated seed, log:\n%s", joined)
	}
	if strings.Contains(joined, "MEMORY SNAPSHOT") {
		t.Fatalf("resuming rotated seed must not re-inject StartupContext:\n%s", joined)
	}
}

func TestCompactionFitExtensionWiredAndNodeVerified(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	d, in, logPath := newFakeDriver(t, "ok", Config{Fingerprint: "fp"})
	defer d.Close()

	if _, err := d.RunTurn(ctx, in); err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	extPath := filepath.Join(filepath.Dir(d.cfg.SessionDir), "compaction-fit.mjs")
	if _, err := os.Stat(extPath); err != nil {
		t.Fatalf("expected compaction-fit.mjs at %s, err=%v", extPath, err)
	}
	joined := strings.Join(readLogLines(logPath), "\n")
	if !strings.Contains(joined, "EXTENSION:"+extPath) {
		t.Fatalf("expected --extension flag pointing to %s in log:\n%s", extPath, joined)
	}

	// Execute the generated ESM extension under Node if available to verify zero-dep runtime behavior.
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node binary not available on PATH")
	}
	script := `
import assert from "node:assert/strict";
const { default: extFactory, fitPiCompactionPreparation, piCompactionFitBudget } = await import(process.env.SUMI_EXT_URL);

// 1. Small preparation is untouched.
const smallPrep = {
  messagesToSummarize: [{ role: "user", content: "hello", timestamp: 1 }],
  turnPrefixMessages: [],
  tokensBefore: 100,
  settings: { reserveTokens: 2000 },
};
assert.equal(fitPiCompactionPreparation(smallPrep, { contextWindow: 100000, maxTokens: 4096 }, "threshold"), null);

// 2. Oversized single message is middle-truncated without mutating the original object,
//    and older messages are dropped with an omitted note while keeping the newest message.
const hugeOrig = { role: "assistant", content: [{ type: "text", text: "A".repeat(40000) }], timestamp: 10 };
const mid1 = { role: "user", content: "B".repeat(9000), timestamp: 20 };
const mid2 = { role: "user", content: "C".repeat(9000), timestamp: 21 };
const mid3 = { role: "user", content: "D".repeat(9000), timestamp: 22 };
const newestOrig = { role: "user", content: "newest-keep-me", timestamp: 30 };
const prep = {
  messagesToSummarize: [hugeOrig, mid1, mid2, mid3, newestOrig],
  turnPrefixMessages: [],
  tokensBefore: 20000,
  settings: { reserveTokens: 2000 },
};
const fit = fitPiCompactionPreparation(prep, { contextWindow: 12000, maxTokens: 2000 }, "threshold");
assert.ok(fit, "expected fit to trim oversized messages");
assert.equal(hugeOrig.content[0].text.length, 40000, "original session message must never be mutated");
assert.ok(fit.truncatedMessages >= 1, "expected oversized single message truncation");
assert.ok(fit.droppedMessages >= 1, "expected oldest messages dropped");
assert.ok(prep.messagesToSummarize[0].content[0].text.includes("left out of this summary"), "expected omitted note");
assert.equal(prep.messagesToSummarize[prep.messagesToSummarize.length - 1].content, "newest-keep-me", "newest message must be kept");

// 3. Overflow trigger clamps budget using 80% of tokensBefore when lower than configured window.
const overflowBudget = piCompactionFitBudget({ tokensBefore: 50000, settings: { reserveTokens: 2000 } }, { contextWindow: 1000000, maxTokens: 4096 }, "overflow");
const thresholdBudget = piCompactionFitBudget({ tokensBefore: 50000, settings: { reserveTokens: 2000 } }, { contextWindow: 1000000, maxTokens: 4096 }, "threshold");
assert.ok(overflowBudget < thresholdBudget, "overflow budget must be tighter than configured window");

// 4. Default extension registers session_before_compact and mutates preparation in place.
let handler = null;
extFactory({ on(name, fn) { if (name === "session_before_compact") handler = fn; } });
assert.ok(handler, "extension must subscribe to session_before_compact");
const livePrep = {
  messagesToSummarize: [
    { role: "user", content: "C".repeat(50000), timestamp: 1 },
    { role: "user", content: "tail", timestamp: 2 },
  ],
  turnPrefixMessages: [],
  tokensBefore: 20000,
  settings: { reserveTokens: 2000 },
};
handler({ preparation: livePrep, reason: "overflow" }, { model: { contextWindow: 100000, maxTokens: 2000 } });
assert.ok(livePrep.messagesToSummarize[0].content.includes("cut by Sumi") || livePrep.messagesToSummarize[0].content[0]?.text?.includes("left out"));
`
	cmd := exec.CommandContext(ctx, nodeBin, "--input-type=module", "-e", script)
	cmd.Env = append(os.Environ(), "SUMI_EXT_URL=file://"+extPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node compaction-fit verification failed: %v\n%s", err, string(out))
	}
}
