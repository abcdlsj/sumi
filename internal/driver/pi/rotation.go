package pi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	// DefaultSessionRotateBytes triggers post-compaction JSONL seed rotation once a Pi
	// session file reaches 30 MiB so compacted-away history stops bloating memory and disk.
	DefaultSessionRotateBytes int64 = 30 << 20

	piSessionSeedVersion    = 3
	piVirtualModelStateType = "pi-virtual-model-state"
	piVirtualModelAPI       = "pi-virtual"
)

type sessionSeedResult struct {
	rotatable      bool
	refusalReason  string
	seedFile       string
	rotatedOldFile string
	content        []byte
	entriesBefore  int
	entriesAfter   int
}

type rawSessionEntry struct {
	id         string
	parentID   string
	hasParent  bool
	entryType  string
	customType string
	obj        map[string]any
}

type preSegmentKind int

const (
	preSegmentHistory preSegmentKind = iota
	preSegmentState
	preSegmentUnknownCustom
	preSegmentUnknownEntry
)

func classifyPreSegment(e rawSessionEntry) (preSegmentKind, string) {
	switch e.entryType {
	case "model_change", "thinking_level_change", "session_info":
		return preSegmentState, e.entryType
	case "custom":
		if e.customType == piVirtualModelStateType {
			var provider, modelID any
			if data, ok := e.obj["data"].(map[string]any); ok {
				provider = data["provider"]
				modelID = data["modelId"]
			}
			keyBytes, _ := json.Marshal([]any{e.customType, provider, modelID})
			return preSegmentState, string(keyBytes)
		}
		return preSegmentUnknownCustom, e.customType
	case "message", "custom_message", "branch_summary", "compaction", "context_edit", "label", "usage":
		return preSegmentHistory, ""
	default:
		return preSegmentUnknownEntry, e.entryType
	}
}

func piSessionRotationPaths(oldFile, sessionID string, now time.Time) (seedFile, rotatedOldFile string) {
	// Match Pi's `<ISO-timestamp-with-dashes>_<sessionId>.jsonl` naming so lexicographical
	// ordering always places the rotated seed after the old file.
	ts := now.UTC().Format("2006-01-02T15-04-05-000Z")
	seedFile = filepath.Join(filepath.Dir(oldFile), fmt.Sprintf("%s_%s.jsonl", ts, sessionID))
	rotatedOldFile = oldFile + ".rotated"
	return seedFile, rotatedOldFile
}

// buildSessionSeed parses a Pi v3 JSONL session file and builds a lean replacement seed
// preserving the same session ID (for provider prompt-cache affinity), the latest state entries
// before the active compaction boundary, and the kept segment verbatim with re-chained parentIds.
func buildSessionSeed(oldFile string, raw []byte, now time.Time) sessionSeedResult {
	if oldFile == "" {
		return sessionSeedResult{refusalReason: "no_session_file"}
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	var lines [][]byte
	for {
		line, err := reader.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			lines = append(lines, trimmed)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sessionSeedResult{refusalReason: "read_error"}
		}
	}
	if len(lines) < 2 {
		return sessionSeedResult{refusalReason: "no_compaction"}
	}

	var header struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		ID      string `json:"id"`
		CWD     string `json:"cwd"`
	}
	if err := json.Unmarshal(lines[0], &header); err != nil || header.Type != "session" || header.ID == "" {
		return sessionSeedResult{refusalReason: "invalid_header"}
	}
	if header.Version != piSessionSeedVersion {
		return sessionSeedResult{refusalReason: "unsupported_session_version"}
	}

	seedFile, rotatedOldFile := piSessionRotationPaths(oldFile, header.ID, now)
	if filepath.Base(seedFile) <= filepath.Base(oldFile) {
		return sessionSeedResult{refusalReason: "seed_name_not_newer"}
	}

	byID := make(map[string]rawSessionEntry, len(lines)-1)
	allEntries := make([]rawSessionEntry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			return sessionSeedResult{refusalReason: "invalid_entry_json"}
		}
		id, _ := obj["id"].(string)
		entryType, _ := obj["type"].(string)
		if id == "" || entryType == "" {
			return sessionSeedResult{refusalReason: "invalid_entry"}
		}
		parentID, hasParent := obj["parentId"].(string)
		customType, _ := obj["customType"].(string)
		entry := rawSessionEntry{
			id:         id,
			parentID:   parentID,
			hasParent:  hasParent && parentID != "",
			entryType:  entryType,
			customType: customType,
			obj:        obj,
		}
		byID[id] = entry
		allEntries = append(allEntries, entry)
	}

	// Reconstruct the active branch from the leaf (last appended entry) back to root.
	branch := make([]rawSessionEntry, 0, len(allEntries))
	visited := make(map[string]struct{}, len(allEntries))
	cur := allEntries[len(allEntries)-1]
	for {
		if _, dup := visited[cur.id]; dup {
			return sessionSeedResult{refusalReason: "branch_cycle"}
		}
		visited[cur.id] = struct{}{}
		branch = append(branch, cur)
		if !cur.hasParent {
			break
		}
		parent, ok := byID[cur.parentID]
		if !ok {
			return sessionSeedResult{refusalReason: "broken_parent_chain"}
		}
		cur = parent
	}
	// Reverse to root -> leaf order.
	for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
		branch[i], branch[j] = branch[j], branch[i]
	}

	for _, e := range branch {
		if kind, _ := classifyPreSegment(e); kind == preSegmentUnknownEntry {
			return sessionSeedResult{refusalReason: "unknown_entry_type"}
		}
	}

	compactionIndex := -1
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i].entryType == "compaction" {
			compactionIndex = i
			break
		}
	}
	if compactionIndex < 0 {
		return sessionSeedResult{refusalReason: "no_compaction"}
	}
	firstKeptID, _ := branch[compactionIndex].obj["firstKeptEntryId"].(string)
	if firstKeptID == "" {
		return sessionSeedResult{refusalReason: "first_kept_entry_not_on_branch"}
	}
	segmentStart := -1
	for i := 0; i <= compactionIndex; i++ {
		if branch[i].id == firstKeptID {
			segmentStart = i
			break
		}
	}
	if segmentStart < 0 {
		return sessionSeedResult{refusalReason: "first_kept_entry_not_on_branch"}
	}

	segment := branch[segmentStart:]
	hasAssistantResponse := false
	for _, e := range segment {
		if e.entryType != "message" {
			continue
		}
		msg, ok := e.obj["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		api, _ := msg["api"].(string)
		if role == "assistant" && api != piVirtualModelAPI {
			hasAssistantResponse = true
			break
		}
	}
	if !hasAssistantResponse {
		return sessionSeedResult{refusalReason: "no_response_in_kept_segment"}
	}

	// Keep the latest state entry per key before segmentStart; refuse unknown custom state.
	lastIdxByKey := make(map[string]int)
	for i := 0; i < segmentStart; i++ {
		kind, key := classifyPreSegment(branch[i])
		switch kind {
		case preSegmentUnknownCustom:
			return sessionSeedResult{refusalReason: "unknown_custom_type"}
		case preSegmentState:
			lastIdxByKey[key] = i
		}
	}
	stateIndices := make([]int, 0, len(lastIdxByKey))
	for _, idx := range lastIdxByKey {
		stateIndices = append(stateIndices, idx)
	}
	sort.Ints(stateIndices)

	kept := make([]rawSessionEntry, 0, len(stateIndices)+len(segment))
	for _, idx := range stateIndices {
		kept = append(kept, branch[idx])
	}
	kept = append(kept, segment...)

	seedHeader := map[string]any{
		"type":          "session",
		"version":       piSessionSeedVersion,
		"id":            header.ID,
		"timestamp":     now.UTC().Format("2006-01-02T15:04:05.000Z"),
		"cwd":           header.CWD,
		"parentSession": rotatedOldFile,
	}
	var buf bytes.Buffer
	headerBytes, err := json.Marshal(seedHeader)
	if err != nil {
		return sessionSeedResult{refusalReason: "marshal_error"}
	}
	buf.Write(headerBytes)
	buf.WriteByte('\n')

	var prevID any = nil
	for _, e := range kept {
		clone := make(map[string]any, len(e.obj))
		for k, v := range e.obj {
			clone[k] = v
		}
		clone["parentId"] = prevID
		prevID = e.id
		lineBytes, err := json.Marshal(clone)
		if err != nil {
			return sessionSeedResult{refusalReason: "marshal_error"}
		}
		buf.Write(lineBytes)
		buf.WriteByte('\n')
	}

	return sessionSeedResult{
		rotatable:      true,
		seedFile:       seedFile,
		rotatedOldFile: rotatedOldFile,
		content:        buf.Bytes(),
		entriesBefore:  len(allEntries),
		entriesAfter:   len(kept),
	}
}

func writeDurableFile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// rotateSessionFile executes the two-step crash-safe JSONL rotation:
//  1. write seed to `.tmp` with fsync and atomically rename into place (`<newTs>_<id>.jsonl`);
//  2. rename old file to `<oldFile>.rotated` (removing the seed if step 2 fails).
//
// Returns the path that future resumes should open and whether rotation moved to the new seed.
func rotateSessionFile(oldFile string, now time.Time) (activeFile string, rotated bool, reason string) {
	raw, err := os.ReadFile(oldFile)
	if err != nil {
		return oldFile, false, "read_failed"
	}
	seed := buildSessionSeed(oldFile, raw, now)
	if !seed.rotatable {
		return oldFile, false, seed.refusalReason
	}
	tmp := seed.seedFile + ".tmp"
	if err := writeDurableFile(tmp, seed.content); err != nil {
		return oldFile, false, "write_seed_failed"
	}
	if err := os.Rename(tmp, seed.seedFile); err != nil {
		_ = os.Remove(tmp)
		return oldFile, false, "rename_seed_failed"
	}
	if err := os.Rename(oldFile, seed.rotatedOldFile); err != nil {
		if rmErr := os.Remove(seed.seedFile); rmErr != nil {
			// Seed remained in place and sorts newer than oldFile; adopt the seed.
			return seed.seedFile, true, ""
		}
		return oldFile, false, "rename_old_failed"
	}
	return seed.seedFile, true, ""
}
