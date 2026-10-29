package ast

import (
	"strings"
	"testing"
)

func diffActions(t *testing.T, e *Engine, path, a, b string) []string {
	t.Helper()
	prev := parseOrFatal(t, e, path, a)
	next := parseOrFatal(t, e, path, b)
	var out []string
	for _, ev := range Diff("r", prev, next).Events {
		out = append(out, string(ev.Action)+" "+ev.Signature)
	}
	return out
}

func wantActions(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q; want %q", got, want)
	}
}

// A regex or nested template is not a comment or an open string: code after
// `/^https?:\/\//`, `/["']/` or `${x ? `a//b` : ”}` is still code.
func TestDiff_RegexAndTemplateLiteralsDoNotHideEdits(t *testing.T) {
	e := newTestEngine(t)
	a := "const isURL = (u) => /^https?:\\/\\//.test(u) ? fetchRemote(u) : readLocal(u);\n"
	wantActions(t, diffActions(t, e, "a.ts", a, strings.Replace(a, "readLocal", "deleteAll", 1)),
		"MODIFIED arrow_function:a.ts::isURL")

	c := "function clean(s) {\n  const t = s.replace(/[\"']/g, \"\");\n  return fetch(\"https://x/v1/\" + t);\n}\n"
	wantActions(t, diffActions(t, e, "b.js", c, strings.Replace(c, "+ t)", "+ s)", 1)),
		"MODIFIED function:b.js::clean")

	g := "const url = (host) => `${host ? `https://${host}` : ''}/api/v1/users`;\n"
	wantActions(t, diffActions(t, e, "c.ts", g, strings.Replace(g, "/api/", "/admin/", 1)),
		"MODIFIED arrow_function:c.ts::url")

	// Comments and whitespace remain cosmetic in the tree-driven mode.
	cosmetic := strings.Replace(c, "  return fetch", "  // fetch it\n  return   fetch", 1)
	wantActions(t, diffActions(t, e, "d.js", c, cosmetic))
}

// Function and class EXPRESSIONS with a stable name are declarations.
func TestDiff_NamedFunctionAndClassExpressionsTracked(t *testing.T) {
	e := newTestEngine(t)
	a := "const handler = function (req, res) {\n  res.send(1);\n};\n"
	wantActions(t, diffActions(t, e, "h.js", a, strings.Replace(a, "send(1)", "send(2)", 1)),
		"MODIFIED function:h.js::handler")

	a = "export default function () {\n  return 1;\n}\n"
	wantActions(t, diffActions(t, e, "d.ts", a, strings.Replace(a, "return 1", "return 2", 1)),
		"MODIFIED function:d.ts::default")

	a = "export default class {\n  run() { return 1; }\n}\n"
	wantActions(t, diffActions(t, e, "k.ts", a, strings.Replace(a, "return 1", "return 2", 1)),
		"MODIFIED class:k.ts::default", "MODIFIED method:k.ts::default.run")

	// An anonymous callback is still not a declaration.
	a = "app.get('/', function (req, res) {\n  res.send(1);\n});\n"
	wantActions(t, diffActions(t, e, "cb.js", a, strings.Replace(a, "send(1)", "send(2)", 1)))
}

// Nested classes carry their owner, so inserting a sibling above leaves the
// untouched ones alone.
func TestDiff_NestedClassesKeyedByOwner(t *testing.T) {
	e := newTestEngine(t)
	a := "class A(Model):\n    class Meta:\n        db_table = 'a'\n\nclass B(Model):\n    class Meta:\n        db_table = 'b'\n"
	b := "class Z(Model):\n    class Meta:\n        db_table = 'z'\n\n" + a
	wantActions(t, diffActions(t, e, "models.py", a, b),
		"ADDED class:models.py::Z", "ADDED class:models.py::Z.Meta")
}

// Same-named declarations are matched by body, not by position.
func TestDiff_OccurrenceSuffixAlignedByBody(t *testing.T) {
	e := newTestEngine(t)
	a := "package p\n\nfunc init() {\n\tregisterA()\n}\n\nfunc init() {\n\tregisterB()\n}\n"
	top := strings.Replace(a, "package p\n\n", "package p\n\nfunc init() {\n\tregisterNew()\n}\n\n", 1)
	wantActions(t, diffActions(t, e, "reg.go", a, top), "ADDED function:reg.go::init")

	// Removing the first deletes the first, not the second.
	first := "func init() {\n\tregisterA()\n}\n\n"
	wantActions(t, diffActions(t, e, "reg2.go", a, strings.Replace(a, first, "", 1)),
		"DELETED function:reg2.go::init")

	// Editing the second one is still a single MODIFIED.
	wantActions(t, diffActions(t, e, "reg3.go", a, strings.Replace(a, "registerB", "registerC", 1)),
		"MODIFIED function:reg3.go::init#2")
}

// A Go interface is a declaration like a struct.
func TestDiff_GoInterfaceTracked(t *testing.T) {
	e := newTestEngine(t)
	a := "package p\n\ntype Store interface {\n\tGet(id string) (Item, error)\n}\n"
	wantActions(t, diffActions(t, e, "store.go", a, strings.Replace(a, "Get(id string)", "Get(ctx Ctx, id string)", 1)),
		"MODIFIED class:store.go::Store")
}
