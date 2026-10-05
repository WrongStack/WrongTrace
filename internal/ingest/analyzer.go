package ingest

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wrongstack/wrongtrace/internal/models"
)

var targetFileKeys = []string{
	"AbsolutePath", "absolute_path", "TargetFile", "target_file", "targetFile", "TargetContent",
	"path", "FilePath", "file_path", "filePath", "filename", "fileName", "file_name", "file", "target", "dest",
	"URI", "uri", "Url", "url", "relative_path", "relativePath", "rel_path",
	"SourceFile", "source_file", "SearchPath", "search_path", "DirectoryPath", "directory_path",
}

var (
	startLineKeys = []string{"StartLine", "start_line", "startLine", "offset", "line_start", "lineStart", "from_line", "fromLine", "start"}
	endLineKeys   = []string{"EndLine", "end_line", "endLine", "line_end", "lineEnd", "to_line", "toLine", "end"}
	countLineKeys = []string{"lines", "line_count", "count", "num_lines", "limit"}

	// Only the top-level "tool_calls" key is read below. The looser `"tool`
	// prefix also matched "tool_result"/"tool_use_id"/"toolUseResult", so
	// every tool-result line -- which embeds whole file contents -- paid a
	// full generic JSON decode and yielded nothing.
	bTool          = []byte(`"tool_calls"`)
	bUserInput     = []byte(`"USER_INPUT"`)
	bModelLower    = []byte(`"model"`)
	bModelUpper    = []byte(`"Model"`)
	bUsage         = []byte(`"usage"`)
	bSelectedModel = []byte(`"selectedModel"`)
	bPlannerModel  = []byte(`"planner_model"`)
)

// ExtractTargetFile attempts to extract the destination file path from a tool call payload.
func ExtractTargetFile(args map[string]interface{}) string {
	for _, k := range targetFileKeys {
		if val, ok := args[k]; ok {
			if s, isStr := val.(string); isStr && s != "" {
				return s
			}
		}
	}
	return ""
}

// ExtractLineRange extracts start line and end line from tool arguments if present.
func ExtractLineRange(args map[string]interface{}) (int, int, int) {
	startLine := 1
	endLine := 0
	linesCount := 0

	for _, k := range startLineKeys {
		if val, ok := args[k]; ok {
			if n := extractInt(val); n > 0 {
				startLine = n
				break
			}
		}
	}

	for _, k := range endLineKeys {
		if val, ok := args[k]; ok {
			if n := extractInt(val); n > 0 {
				endLine = n
				break
			}
		}
	}

	for _, k := range countLineKeys {
		if val, ok := args[k]; ok {
			if n := extractInt(val); n > 0 {
				linesCount = n
				break
			}
		}
	}

	if linesCount == 0 && endLine >= startLine {
		linesCount = endLine - startLine + 1
	}
	if endLine == 0 && linesCount > 0 {
		remaining := int(^uint(0)>>1) - startLine + 1
		if linesCount > remaining {
			linesCount = remaining
		}
		endLine = startLine + (linesCount - 1)
	}

	return startLine, endLine, linesCount
}

func extractInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		if parsed, ok := jsonNumberInt64(n); ok && int64(int(parsed)) == parsed {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return parsed
		}
	}
	return 0
}

// ParseJSONLTranscript parses a JSONL file line-by-line, extracting file modifying tool calls.
func ParseJSONLTranscript(filePath string) ([]ToolCallEvent, error) {
	modEvents, _, err := ParseJSONLTranscriptFull(filePath)
	return modEvents, err
}

// ParseJSONLTranscriptFull parses a JSONL file line-by-line from start to finish.
func ParseJSONLTranscriptFull(filePath string) ([]ToolCallEvent, []FileReadEvent, error) {
	modEvents, readEvents, _, err := ParseJSONLTranscriptFromOffset(filePath, 0)
	return modEvents, readEvents, err
}

// ParseJSONLTranscriptFromOffset streams new records from startOffset and
// recovers preceding model/intent without materializing historical events.
func ParseJSONLTranscriptFromOffset(filePath string, startOffset int64) ([]ToolCallEvent, []FileReadEvent, int64, error) {
	if startOffset < 0 {
		return nil, nil, startOffset, fmt.Errorf("invalid transcript offset: %d", startOffset)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, startOffset, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return parseJSONLFile(f, filePath, startOffset)
}

// jsonlCursor is the resumable position in a JSONL transcript: the committed
// offset, its preceding-byte fingerprint and inherited model/intent.
type jsonlCursor struct {
	offset         int64
	fingerprint    uint64
	hasFingerprint bool
	context        jsonlContext
}

type jsonlContext struct {
	ModelName string `json:"model"`
	Intent    string `json:"intent,omitempty"`
}

// fingerprintBytes is how much of the committed prefix identifies the file.
// The bytes right before the offset are the tail of the last record read, so
// a transcript replaced by different content almost never reproduces them.
const fingerprintBytes = 64

// tailFingerprint hashes (FNV-1a) up to fingerprintBytes bytes ending at
// offset. ok is false when they cannot be read (the file is now shorter).
func tailFingerprint(f *os.File, offset int64) (uint64, bool) {
	if offset <= 0 {
		return 0, false
	}
	n := min(offset, fingerprintBytes)
	var buf [fingerprintBytes]byte
	if _, err := f.ReadAt(buf[:n], offset-n); err != nil {
		return 0, false
	}
	h := uint64(14695981039346656037)
	for _, b := range buf[:n] {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return h, true
}

// parseJSONLResumable resumes a transcript at cur, restarting from byte 0
// when the stored fingerprint no longer matches: the file was replaced by a
// different (and larger, or equal-sized) one, and resuming at the old offset
// would start mid-line in unrelated content and drop its first records.
// Cursors without a fingerprint (checkpoints from before fingerprints
// existed) are trusted as before.
func parseJSONLResumable(filePath string, cur jsonlCursor) ([]ToolCallEvent, []FileReadEvent, jsonlCursor, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, cur, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	start := cur.offset
	if start > 0 && cur.hasFingerprint {
		if fp, ok := tailFingerprint(f, start); !ok || fp != cur.fingerprint {
			start = 0
		}
	}
	context := cur.context
	if start == 0 {
		context = jsonlContext{}
	}
	mods, reads, committed, err := parseJSONLFile(f, filePath, start, &context)
	next := jsonlCursor{offset: committed, context: context}
	if err == nil {
		next.fingerprint, next.hasFingerprint = tailFingerprint(f, committed)
	}
	return mods, reads, next, err
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func parseJSONLFile(f *os.File, filePath string, startOffset int64, inherited ...*jsonlContext) ([]ToolCallEvent, []FileReadEvent, int64, error) {
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return nil, nil, startOffset, fmt.Errorf("seek transcript: %w", err)
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	var modEvents []ToolCallEvent
	var readEvents []FileReadEvent
	sessionID := sessionIDForPath(filePath)
	agentName := detectAgentFromPath(filePath)
	context := jsonlContext{ModelName: detectAgentDefaultModel(agentName)}
	if startOffset > 0 {
		if len(inherited) > 0 && inherited[0] != nil && inherited[0].ModelName != "" {
			context = *inherited[0]
		} else {
			var err error
			context, err = readJSONLContext(f, startOffset, context)
			if err != nil {
				return nil, nil, startOffset, fmt.Errorf("read transcript context: %w", err)
			}
		}
	}
	defer func() {
		if len(inherited) > 0 && inherited[0] != nil {
			*inherited[0] = context
		}
	}()
	currentOffset := startOffset
	committed := startOffset // offset through the last '\n'-terminated line

	for {
		lineStart := currentOffset
		lineBytes, err := reader.ReadBytes('\n')
		readLen := int64(len(lineBytes))
		currentOffset += readLen

		complete := err == nil
		if complete {
			committed = currentOffset
		} else if err != io.EOF {
			return modEvents, readEvents, committed, err
		}
		// An unterminated tail (io.EOF) is committed only when it is already a
		// complete JSON value: a writer that is still mid-line cannot have
		// produced one (an object is not valid until its closing brace), and
		// leaving a finished record uncommitted re-parsed and RE-EMITTED its
		// tool calls on every poll until some later newline arrived — forever,
		// for a transcript whose final record has no trailing newline. A
		// partial tail is neither committed nor able to emit anything, so the
		// next poll re-reads it once complete.
		if lineStart == 0 {
			// A UTF-8 BOM made the first record invalid JSON, silently
			// dropping it.
			lineBytes = bytes.TrimPrefix(lineBytes, utf8BOM)
		}
		trimmed := bytes.TrimSpace(lineBytes)
		if !complete && len(trimmed) > 0 && json.Valid(trimmed) {
			committed = currentOffset
		}
		if len(trimmed) > 0 {
			// Fast pre-filter: skip JSON unmarshaling for lines that cannot contain tool calls, user input, model info, or usage
			if !jsonlRowHasSignals(trimmed) {
				if !complete {
					break
				}
				continue
			}

			if row, uErr := decodeJSONLRow(trimmed); uErr == nil {
				updateJSONLContext(&context, row)

				// Extract usage tokens if present
				var promptTokens, completionTokens int64
				if usage, ok := row["usage"].(map[string]interface{}); ok {
					promptTokens = extractInt64(usage, "input_tokens", "prompt_tokens", "promptTokenCount", "inputTokens")
					completionTokens = extractInt64(usage, "output_tokens", "completion_tokens", "candidatesTokenCount", "outputTokens")
				}

				// Extract tool calls
				if toolCalls, ok := row["tool_calls"].([]interface{}); ok {
					readIndex := 0
					for _, tc := range toolCalls {
						tcMap, ok := tc.(map[string]interface{})
						if !ok {
							continue
						}

						name, _ := tcMap["name"].(string)
						if name == "" {
							name, _ = tcMap["tool"].(string)
						}

						isMod := IsFileModifyingTool(name)
						isRead := IsFileReadingTool(name)
						if !isMod && !isRead {
							continue
						}

						// Check if tool call has its own specific model override
						tcModel := context.ModelName
						if m := extractModelFromRow(tcMap); m != "" {
							tcModel = m
						}

						// Extract args
						var args map[string]interface{}
						if a, ok := tcMap["args"].(map[string]interface{}); ok {
							args = a
						} else if a, ok := tcMap["parameters"].(map[string]interface{}); ok {
							args = a
						}

						targetFile := ExtractTargetFile(args)
						cost := models.Global.CalculateCost(tcModel, promptTokens, completionTokens)

						occurredAt := time.Now().UTC()
						if created, ok := row["created_at"].(string); ok {
							if t, err := time.Parse(time.RFC3339, created); err == nil {
								occurredAt = t
							}
						}

						if isMod {
							ev := ToolCallEvent{
								SessionID:        sessionID,
								AgentName:        agentName,
								ModelName:        tcModel,
								ToolName:         name,
								TargetFile:       targetFile,
								PromptTokens:     promptTokens,
								CompletionTokens: completionTokens,
								CostUSD:          cost,
								Intent:           context.Intent,
								OccurredAt:       occurredAt,
							}
							modEvents = append(modEvents, ev)
						}

						if isRead && targetFile != "" {
							sLine, eLine, lCount := ExtractLineRange(args)
							readID := fmt.Sprintf("read-%s-%d", sessionID, lineStart)
							if readIndex > 0 {
								readID += fmt.Sprintf("-%d", readIndex)
							}
							readIndex++
							rEv := FileReadEvent{
								// Keyed by the line's byte offset so the ID is
								// stable across re-reads (dedup on the PK) and
								// unique per session file. A per-batch counter
								// collided across polls, silently discarding
								// every batch after the first.
								// Additional reads in the same row need distinct IDs too.
								ReadID:         readID,
								SessionID:      sessionID,
								FilePath:       targetFile,
								AgentName:      agentName,
								ModelName:      tcModel,
								ToolName:       name,
								StartLine:      sLine,
								EndLine:        eLine,
								LinesReadCount: lCount,
								PromptTokens:   promptTokens,
								CostUSD:        cost,
								Intent:         context.Intent,
								OccurredAt:     occurredAt,
							}
							readEvents = append(readEvents, rEv)
						}
					}
				}
			}
		}
		if !complete {
			// Unterminated tail reached EOF: stop after best-effort parsing.
			break
		}
	}

	return modEvents, readEvents, committed, nil
}

func jsonlRowHasSignals(line []byte) bool {
	return bytes.Contains(line, bTool) || bytes.Contains(line, bUserInput) ||
		bytes.Contains(line, bModelLower) || bytes.Contains(line, bModelUpper) ||
		bytes.Contains(line, bUsage) || bytes.Contains(line, bSelectedModel) ||
		bytes.Contains(line, bPlannerModel) || bytes.Contains(line, []byte(`\u`))
}

func updateJSONLContext(context *jsonlContext, row map[string]interface{}) {
	if model := extractModelFromRow(row); model != "" && !models.IsJunkModel(model) {
		context.ModelName = model
	}
	if intent, ok := row["content"].(string); ok && row["type"] == "USER_INPUT" {
		context.Intent = runeSafeTruncate(intent, 80)
	}
}

// Recover metadata for legacy cursors and stateless offset callers without
// constructing or replaying historical tool/read events. ReadAt leaves the
// main parser's file position unchanged.
func readJSONLContext(f *os.File, end int64, context jsonlContext) (jsonlContext, error) {
	reader := bufio.NewReaderSize(io.NewSectionReader(f, 0, end), 64*1024)
	first := true
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return context, err
		}
		if first {
			line = bytes.TrimPrefix(line, utf8BOM)
			first = false
		}
		if trimmed := bytes.TrimSpace(line); jsonlRowHasSignals(trimmed) {
			if row, decodeErr := decodeJSONLRow(trimmed); decodeErr == nil {
				updateJSONLContext(&context, row)
			}
		}
		if err == io.EOF {
			return context, nil
		}
	}
}

// sessionIDForPath derives a stable, collision-free session identifier from
// the transcript path. Every agent session directory stores its transcript
// under the same file name (transcript.jsonl), so using only the base name
// collapses all sessions into one session id — and via ReportRun into a
// single agent_runs row. Including the parent directory (the session UUID or
// date folder) supplies a readable prefix; the full path fingerprint separates
// identical directory/file names belonging to different transcript owners.
func sessionIDForPath(filePath string) string {
	path, err := filepath.Abs(filePath)
	if err != nil {
		path = filepath.Clean(filePath)
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	parent := filepath.Base(filepath.Dir(path))
	prefix := base
	if parent == "" || parent == "." || parent == string(filepath.Separator) || parent == "/" {
		prefix = base
	} else {
		prefix = parent + "-" + base
	}
	return fmt.Sprintf("%s-%x", prefix, sha256.Sum256([]byte(path)))
}

// ParseClineTask parses a Cline / Roo Code task JSON structure.
func ParseClineTask(filePath string) ([]ToolCallEvent, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read cline task: %w", err)
	}
	// Events come only from messages[].say. Other agents' session files that
	// share the tasks/sessions/conversations layout (Continue, Zed) never
	// carry that key, yet are rewritten on every message; decoding each whole
	// file into nested maps produced nothing.
	if !bytes.Contains(data, []byte(`"say"`)) {
		return nil, nil
	}

	var root map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("unmarshal cline task: %w", err)
	}
	if len(bytes.TrimSpace(data[decoder.InputOffset():])) != 0 {
		return nil, fmt.Errorf("unmarshal cline task: trailing data")
	}

	sessionID := sessionIDForPath(filePath)
	modelName := "unknown-model"
	if m := extractModelFromRow(root); m != "" {
		modelName = m
	}

	promptTokens := extractInt64(root, "tokensIn", "promptTokens", "input_tokens")
	completionTokens := extractInt64(root, "tokensOut", "completionTokens", "output_tokens")
	cost := models.Global.CalculateCost(modelName, promptTokens, completionTokens)

	var events []ToolCallEvent
	if messages, ok := root["messages"].([]interface{}); ok {
		for _, msg := range messages {
			mMap, ok := msg.(map[string]interface{})
			if !ok {
				continue
			}
			say, _ := mMap["say"].(string)
			if say == "tool" || say == "command" {
				text, _ := mMap["text"].(string)
				// Cline/Roo stamp every message (`ts`, epoch milliseconds).
				occurredAt := time.Now().UTC()
				if ts := extractInt64(mMap, "ts"); ts > 0 {
					occurredAt = time.UnixMilli(ts).UTC()
				}
				events = append(events, ToolCallEvent{
					SessionID:        sessionID,
					AgentName:        "Cline/Roo",
					ModelName:        modelName,
					ToolName:         say,
					TargetFile:       text,
					PromptTokens:     promptTokens,
					CompletionTokens: completionTokens,
					CostUSD:          cost,
					OccurredAt:       occurredAt,
				})
			}
		}
	}

	return events, nil
}

// ParseAiderHistory parses an Aider Markdown history log.
func ParseAiderHistory(filePath string) ([]ToolCallEvent, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read aider history: %w", err)
	}

	content := string(data)
	var events []ToolCallEvent

	model := "unknown-model"
	if match := aiderModelRe.FindStringSubmatch(content); len(match) > 1 {
		model = match[1]
	}

	fileMatches := aiderFileRe.FindAllStringSubmatch(content, -1)
	for _, fm := range fileMatches {
		if len(fm) > 1 {
			events = append(events, ToolCallEvent{
				// Derived from the path like every other parser: a constant
				// here collapsed every workspace's aider history into one
				// session id, which ReportRun upserts onto a single
				// agent_runs row — the last ingested workspace erased all
				// the others (see sessionIDForPath).
				SessionID:  sessionIDForPath(filePath),
				AgentName:  "Aider",
				ModelName:  model,
				ToolName:   "apply_diff",
				TargetFile: fm[1],
				CostUSD:    0.0,
				OccurredAt: time.Now().UTC(),
			})
		}
	}

	return events, nil
}

var (
	aiderModelRe     = regexp.MustCompile(`(?i)Model:\s*([\w\.\-]+)`)
	aiderFileRe      = regexp.MustCompile(`(?i)(?:Applied edit to|Updated|Created|Modified)\s*([\w\.\/\\]+)`)
	modelSelectionRe = regexp.MustCompile(`(?i)(?:Model Selection|Active Model)[\x60'\s:]+(?:from\s+[^\n]+?\s+)?to\s+([A-Za-z0-9\.\-_ ]+?)(?:\s*\(|\n|$)`)
	modelTagRe       = regexp.MustCompile(`(?i)<(?:model|model_name)>([^<]+)</(?:model|model_name)>|<!--\s*model:\s*([a-zA-Z0-9\.\-_/]+)\s*-->`)
)

var (
	modelKeys       = []string{"model", "model_name", "modelName", "model_id", "modelId", "apiModelId", "selectedModel", "planner_model", "llm_model", "wire_model"}
	modelNestedKeys = []string{"metadata", "params", "options", "config", "response", "system_info", "args"}
)

// jsonlRowKeys is every top-level key parseJSONLResumable and
// extractModelFromRow read from a transcript line.
var jsonlRowKeys = func() map[string]struct{} {
	keys := map[string]struct{}{
		"content": {}, "type": {}, "usage": {}, "tool_calls": {}, "created_at": {},
	}
	for _, k := range modelKeys {
		keys[k] = struct{}{}
	}
	for _, k := range modelNestedKeys {
		keys[k] = struct{}{}
	}
	return keys
}()

// decodeJSONLRow decodes one transcript line into the same map a plain
// json.Unmarshal would produce, restricted to jsonlRowKeys. Claude Code lines
// pass the pre-filter on "model" yet carry their payload under keys the parser
// never reads ("message", "toolUseResult") -- often whole file contents.
// Decoding those into nested maps was the tailer's dominant allocation; here
// they stay raw bytes and are dropped. Key matching and duplicate-key handling
// are those of map decoding, so the parser sees an identical row.
func decodeJSONLRow(line []byte) (map[string]interface{}, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	row := make(map[string]interface{}, 4)
	for k, v := range raw {
		if _, ok := jsonlRowKeys[k]; !ok {
			continue
		}
		var val interface{}
		decoder := json.NewDecoder(bytes.NewReader(v))
		decoder.UseNumber()
		if err := decoder.Decode(&val); err != nil {
			return nil, err
		}
		row[k] = val
	}
	return row, nil
}

func extractModelFromRow(m map[string]interface{}) string {
	for _, k := range modelKeys {
		if val, ok := m[k]; ok {
			if s, isStr := val.(string); isStr && s != "" && s != "inherit" {
				if norm := normalizeModelName(s); norm != "" {
					return norm
				}
			}
		}
	}

	for _, nk := range modelNestedKeys {
		if sub, ok := m[nk].(map[string]interface{}); ok {
			for _, k := range modelKeys {
				if val, ok := sub[k]; ok {
					if s, isStr := val.(string); isStr && s != "" && s != "inherit" {
						if norm := normalizeModelName(s); norm != "" {
							return norm
						}
					}
				}
			}
			if val, ok := sub["Model"]; ok {
				if s, isStr := val.(string); isStr && s != "" && s != "inherit" {
					if norm := normalizeModelName(s); norm != "" {
						return norm
					}
				}
			}
		}
	}

	// Extract from content text (e.g. Antigravity settings changes or prompt headers)
	if content, ok := m["content"].(string); ok && content != "" {
		if strings.Contains(content, "model") || strings.Contains(content, "Model") || strings.Contains(content, "MODEL") || strings.Contains(content, "<!--") {
			if match := modelSelectionRe.FindStringSubmatch(content); len(match) > 1 {
				extracted := strings.TrimSpace(match[1])
				if extracted != "" && !strings.EqualFold(extracted, "none") {
					if norm := normalizeModelName(extracted); norm != "" {
						return norm
					}
				}
			}
			if match := modelTagRe.FindStringSubmatch(content); len(match) > 0 {
				for i := 1; i < len(match); i++ {
					if match[i] != "" {
						extracted := strings.TrimSpace(match[i])
						if norm := normalizeModelName(extracted); norm != "" {
							return norm
						}
					}
				}
			}
		}
	}

	return ""
}

func normalizeModelName(raw string) string {
	raw = strings.TrimSpace(raw)

	// Clean trailing annotations like "(Medium)" or "(Default)" BEFORE the junk
	// gate. IsJunkModel rejects any string containing '(', so running the gate
	// first made this strip unreachable and silently discarded every annotated
	// model name -- transcripts that report "Gemini 3.7 Flash (Medium)" were
	// attributed to unknown-model and priced on the fallback estimate. The gate
	// below still runs, now on the cleaned name, so genuine junk
	// ("this.model (x)", a leading "(Preview)") stays rejected.
	if idx := strings.Index(raw, "("); idx > 0 {
		raw = strings.TrimSpace(raw[:idx])
	}
	if models.IsJunkModel(raw) {
		return ""
	}
	lower := strings.ToLower(raw)

	// Canonical mapping for common display names
	switch {
	case lower == "gemini 3.7 flash":
		return "gemini-3.7-flash"
	case lower == "gemini 2.5 pro":
		return "gemini-2.5-pro"
	case lower == "gemini 2.0 flash":
		return "gemini-2.0-flash"
	case lower == "gemini 1.5 pro":
		return "gemini-1.5-pro"
	case lower == "claude 3.7 sonnet" || lower == "claude-3-7-sonnet":
		return "claude-3-7-sonnet"
	case lower == "claude 3.5 sonnet" || lower == "claude-3-5-sonnet":
		return "claude-3-5-sonnet"
	case lower == "deepseek v3" || lower == "deepseek-v3":
		return "deepseek-v3"
	case lower == "deepseek r1" || lower == "deepseek-r1":
		return "deepseek-r1"
	case lower == "gpt-4o-mini" || lower == "gpt-4o mini":
		return "gpt-4o-mini"
	case lower == "gpt-4o":
		return "gpt-4o"
	case lower == "o3-mini":
		return "o3-mini"
	default:
		// Slugify human readable string: "Gemini Pro" -> "gemini-pro"
		slug := strings.ReplaceAll(lower, " ", "-")
		if models.IsJunkModel(slug) {
			return ""
		}
		return slug
	}
}

func extractInt64(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			switch v := val.(type) {
			case json.Number:
				if parsed, ok := jsonNumberInt64(v); ok {
					return parsed
				}
			case float64:
				return int64(v)
			case int64:
				return v
			case int:
				return int64(v)
			}
		}
	}
	return 0
}

func jsonNumberInt64(n json.Number) (int64, bool) {
	if parsed, err := n.Int64(); err == nil {
		return parsed, true
	}
	// Preserve decimal/exponent coercion without rounding through float64.
	parsed, _, err := big.ParseFloat(n.String(), 10, 64, big.ToZero)
	if err == nil && parsed.Cmp(big.NewFloat(-0x1p63)) >= 0 && parsed.Cmp(big.NewFloat(0x1p63)) < 0 {
		integer, _ := parsed.Int64()
		return integer, true
	}
	return 0, false
}

// containsAgentToken reports whether an agent key occurs in the lowered path as a
// whole token: the neighbouring bytes must not be letters. Digits, punctuation and
// '_' count as boundaries, so legitimate install directories still match --
// ".claude", "github-copilot", the versioned "abab6.5s", the dotted "z.ai" -- while
// a key buried inside an ordinary English word does not ("roo" in "classroom",
// "zed" in "customized", "v0" in "srv01", "cline" in "incline"). Bytes >= 0x80 are
// treated as letters so a non-ASCII path segment cannot masquerade as a separator.
func containsAgentToken(haystack, needle string) bool {
	if needle == "" || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] != needle {
			continue
		}
		if i > 0 && isAgentTokenLetter(haystack[i-1]) {
			continue
		}
		if end := i + len(needle); end < len(haystack) && isAgentTokenLetter(haystack[end]) {
			continue
		}
		return true
	}
	return false
}

// isAgentTokenLetter reports whether b continues a word. Only ASCII letters and the
// bytes of a multi-byte rune do; digits and punctuation break a word.
func isAgentTokenLetter(b byte) bool {
	return b >= 0x80 || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func detectAgentFromPath(p string) string {
	// Agent keys are matched at word boundaries rather than as raw substrings: a
	// transcript stored under a directory like "classroom" or "customized-theme"
	// says nothing about which agent produced it, and the value is persisted as
	// ToolCallEvent.AgentName / FileReadEvent.AgentName. Branch ORDER is
	// significant -- it resolves paths that legitimately mention several products
	// -- so only the matching predicate changed.
	lower := strings.ToLower(p)
	switch {
	case containsAgentToken(lower, "wrongstack"):
		return "WrongStack"
	case containsAgentToken(lower, "antigravity") || containsAgentToken(lower, "gemini"):
		return "Antigravity"
	case containsAgentToken(lower, "claude"):
		return "Claude Code"
	case containsAgentToken(lower, "cline") || containsAgentToken(lower, "roo"):
		return "Cline/Roo"
	case containsAgentToken(lower, "replit"):
		return "Replit Agent"
	case containsAgentToken(lower, "zed"):
		return "Zed AI"
	case containsAgentToken(lower, "zcode") || containsAgentToken(lower, "z.ai"):
		return "ZCode"
	case containsAgentToken(lower, "minimax") || containsAgentToken(lower, "abab"):
		return "MiniMax Code"
	case containsAgentToken(lower, "kimi") || containsAgentToken(lower, "moonshot"):
		return "Kimi Code"
	case containsAgentToken(lower, "devin"):
		return "Devin"
	case containsAgentToken(lower, "trae"):
		return "Trae"
	case containsAgentToken(lower, "copilot") || containsAgentToken(lower, "github-copilot"):
		return "GitHub Copilot"
	case containsAgentToken(lower, "openhands") || containsAgentToken(lower, "opendevin"):
		return "OpenHands"
	case containsAgentToken(lower, "goose"):
		return "Goose"
	case containsAgentToken(lower, "cursor"):
		return "Cursor"
	case containsAgentToken(lower, "windsurf") || containsAgentToken(lower, "codeium"):
		return "Windsurf"
	case containsAgentToken(lower, "aider"):
		return "Aider"
	case containsAgentToken(lower, "continue"):
		return "Continue.dev"
	case containsAgentToken(lower, "tabnine"):
		return "Tabnine"
	case containsAgentToken(lower, "bolt"):
		return "Bolt.new"
	case containsAgentToken(lower, "lovable"):
		return "Lovable"
	case containsAgentToken(lower, "v0"):
		return "v0.dev"
	case containsAgentToken(lower, "plandex"):
		return "Plandex"
	case containsAgentToken(lower, "sweep"):
		return "Sweep"
	default:
		return "Coding Agent"
	}
}

func detectAgentDefaultModel(agentName string) string {
	_ = agentName
	return "unknown-model"
}

func runeSafeTruncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i] + "…"
		}
		count++
	}
	return s
}
