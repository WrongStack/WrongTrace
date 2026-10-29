package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/wrongstack/wrongtrace/internal/ast"
	"github.com/wrongstack/wrongtrace/internal/core"
	"github.com/wrongstack/wrongtrace/internal/db"
	"github.com/wrongstack/wrongtrace/internal/ipc"
)

// TestDocsSchemaTableList pins the database section of docs/architecture.md
// against the DDL that actually ships. The doc named a `file_locks` table that
// exists nowhere in the repo — guardrail locks live in an in-memory map — and
// omitted `file_read_events`, which is a real table.
func TestDocsSchemaTableList(t *testing.T) {
	doc := readDocFile(t, "architecture.md")
	ddl := readRepoFile(t, "internal", "db", "schema.sql")

	shipped := map[string]struct{}{}
	for _, m := range regexp.MustCompile(`(?i)CREATE TABLE IF NOT EXISTS\s+(\w+)`).FindAllStringSubmatch(ddl, -1) {
		shipped[m[1]] = struct{}{}
	}
	if len(shipped) == 0 {
		t.Fatal("schema.sql declares no tables; this guard cannot check anything")
	}

	section := doc
	if at := strings.Index(doc, "## 3. Database Schema"); at >= 0 {
		section = doc[at:]
	} else {
		t.Fatal("architecture.md has no '## 3. Database Schema' section")
	}
	documented := map[string]struct{}{}
	for _, m := range regexp.MustCompile("\\*\\s+\\*\\*`([a-z_]+)`\\*\\*").FindAllStringSubmatch(section, -1) {
		documented[m[1]] = struct{}{}
	}
	if len(documented) == 0 {
		t.Fatal("the database section lists no tables; this guard cannot check anything")
	}

	for name := range documented {
		if _, ok := shipped[name]; !ok {
			t.Errorf("architecture.md documents a %q table that schema.sql does not declare (shipped: %s)",
				name, strings.Join(sortedKeys(shipped), ", "))
		}
	}
	for name := range shipped {
		if _, ok := documented[name]; !ok {
			t.Errorf("schema.sql declares %q but architecture.md's schema section never mentions it",
				name)
		}
	}
}

// TestDocsHealthFormulaMatchesCode pins the per-edit penalty both docs state
// against the one the implementation applies. The API reference and the
// architecture doc had drifted to a `\times 15` formula (with a lock penalty
// that does not exist) while FileHealth charges `edits * 8`.
func TestDocsHealthFormulaMatchesCode(t *testing.T) {
	code := readRepoFile(t, "internal", "db", "queries.go")
	m := regexp.MustCompile(`edits \* (\d+)`).FindStringSubmatch(code)
	if m == nil {
		t.Fatal("queries.go no longer applies an `edits * N` penalty; update this guard")
	}
	want := m[1]

	for _, doc := range []string{"api.md", "architecture.md"} {
		body := readDocFile(t, doc)
		// Scope to the HealthScore formula line: the survival-rate formula in
		// the same documents legitimately says `\times 100` for a percentage.
		line := ""
		for _, l := range strings.Split(body, "\n") {
			if strings.Contains(l, "HealthScore") {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("%s states no HealthScore formula; FileHealth applies edits * %s", doc, want)
			continue
		}
		matches := regexp.MustCompile(`\\times\s*(\d+)`).FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			t.Errorf("%s states no per-edit penalty (\times N); FileHealth applies edits * %s", doc, want)
			continue
		}
		for _, got := range matches {
			if got[1] != want {
				t.Errorf("%s states a per-edit penalty of %s, but FileHealth applies edits * %s",
					doc, got[1], want)
			}
		}
	}
}

func readDocFile(t *testing.T, name string) string {
	t.Helper()
	return readRepoFile(t, "docs", name)
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", ".."}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestDocsWireContract pins docs/api.md's JSON examples against the wire types
// the daemon actually serves.
//
// The reference is the published contract integrators code against, and it had
// drifted: the event example named `loc`/`body_hash` where the wire emits
// lines_of_code/ast_content_hash, the friction report was shown with a
// `summary` object it does not have, and the WebSocket examples wrapped their
// payload in `data` where the daemon sends `payload`. A client written against
// any of those reads undefined fields.
//
// The check is tag-level: it reads the names the Go types SERIALIZE (their json
// tags), so an omitempty field still counts as present on the wire.
func TestDocsWireContract(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "api.md"))
	if err != nil {
		t.Fatalf("read docs/api.md: %v", err)
	}

	cases := []struct {
		heading string
		wire    reflect.Type
		// nested maps a documented object/array member to the type its
		// contents are serialized from.
		nested map[string]reflect.Type
	}{
		{heading: "### 2.2.", wire: reflect.TypeOf([]db.EventRecord{})},
		{
			heading: "### 2.3.",
			wire:    reflect.TypeOf(db.InterAgentFrictionReport{}),
			nested: map[string]reflect.Type{
				"edges":             reflect.TypeOf(db.ModelFrictionEdge{}),
				"recent_collisions": reflect.TypeOf(db.CrossThrashEvent{}),
			},
		},
		{heading: "### 2.5.", wire: reflect.TypeOf(ipc.FileHealthReply{})},
		{
			heading: "### `code_event`",
			wire:    reflect.TypeOf(core.WSEvent{}),
			nested:  map[string]reflect.Type{"payload": reflect.TypeOf(ast.Event{})},
		},
		{
			heading: "### `run_reported`",
			wire:    reflect.TypeOf(core.WSEvent{}),
			nested:  map[string]reflect.Type{"payload": reflect.TypeOf(db.RunRecord{})},
		},
	}

	for _, tc := range cases {
		t.Run(tc.heading, func(t *testing.T) {
			documented := docExampleObject(t, string(doc), tc.heading)
			assertKeysOnWire(t, tc.heading, documented, tc.wire, tc.nested)
		})
	}
}

// docExampleObject returns the first JSON example object after heading, taking
// the first element when the example is an array.
func docExampleObject(t *testing.T, doc, heading string) map[string]interface{} {
	t.Helper()
	at := strings.Index(doc, heading)
	if at < 0 {
		t.Fatalf("docs/api.md has no section %q", heading)
	}
	rest := doc[at:]
	open := strings.Index(rest, "```json")
	if open < 0 {
		t.Fatalf("section %q has no ```json example", heading)
	}
	body := rest[open+len("```json"):]
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}

	var decoded interface{}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("section %q example is not valid JSON: %v", heading, err)
	}
	switch v := decoded.(type) {
	case map[string]interface{}:
		return v
	case []interface{}:
		if len(v) == 0 {
			t.Fatalf("section %q example array is empty", heading)
		}
		obj, ok := v[0].(map[string]interface{})
		if !ok {
			t.Fatalf("section %q example array element is not an object", heading)
		}
		return obj
	default:
		t.Fatalf("section %q example is neither an object nor an array", heading)
		return nil
	}
}

func assertKeysOnWire(t *testing.T, heading string, documented map[string]interface{}, wire reflect.Type, nested map[string]reflect.Type) {
	t.Helper()
	onWire := jsonTagNames(wire)
	for key, value := range documented {
		if sub, ok := nested[key]; ok {
			child, isObject := value.(map[string]interface{})
			if !isObject {
				if list, isList := value.([]interface{}); isList && len(list) > 0 {
					child, isObject = list[0].(map[string]interface{})
				}
			}
			if !isObject {
				continue
			}
			assertKeysOnWire(t, heading+" "+key, child, sub, nil)
			continue
		}
		if _, ok := onWire[key]; !ok {
			t.Errorf("docs/api.md %s documents %q, but %s does not serialize it (wire fields: %s)",
				heading, key, wire, strings.Join(sortedKeys(onWire), ", "))
		}
	}
}

// jsonTagNames lists the names a type serializes: a struct's json tags, or a
// slice's element type.
func jsonTagNames(rt reflect.Type) map[string]struct{} {
	for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array {
		rt = rt.Elem()
	}
	names := map[string]struct{}{}
	if rt.Kind() != reflect.Struct {
		return names
	}
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if name := strings.Split(tag, ",")[0]; name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
