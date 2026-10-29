package ast

import (
	"strings"
	"testing"
)

func diffEvents(t *testing.T, path, before, after string) []Event {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatal(err)
	}
	s1, err := e.Parse(path, []byte(before))
	if err != nil {
		t.Fatal(err)
	}
	s2, err := e.Parse(path, []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	return Diff("p", s1, s2).Events
}

func hasModified(evs []Event, sig string) bool {
	for _, ev := range evs {
		if ev.Signature == sig && ev.Action == ActionModified {
			return true
		}
	}
	return false
}

// Arrow functions named by a member target, an object key or a JSX attribute
// keep their identity when unrelated code above them changes; they were keyed
// by byte offset and came back as DELETED+ADDED pairs.
func TestParse_ArrowFunctionIdentitySurvivesEditsAbove(t *testing.T) {
	src := "module.exports.h = () => { return 1; };\n" +
		"const api = { get: () => 2 };\n" +
		"export function C() { return <b onClick={() => go()} />; }\n"
	if evs := diffEvents(t, "h.tsx", src, "// header\nimport x from 'x';\n"+src); len(evs) != 0 {
		t.Fatalf("an edit above unchanged arrow functions emitted %d events: %+v", len(evs), evs)
	}
}

// Declarations sharing a name in one scope each keep their own node, so an
// edit to the first one is not lost behind the second.
func TestParse_SameNamedDeclarationsDoNotOverwrite(t *testing.T) {
	cases := []struct{ path, src, from, to, sig string }{
		{"c.ts", "class C {\n  get x(): number { return this._x; }\n  set x(v: number) { this._x = v; }\n}\n",
			"return this._x;", "return this._x + 1;", "method:c.ts::C.x"},
		{"c.py", "class C:\n    @property\n    def x(self):\n        return self._x\n\n    @x.setter\n    def x(self, v):\n        self._x = v\n",
			"return self._x", "return self._x + 1", "function:c.py::C.x"},
		{"i.go", "package p\n\nfunc init() { a() }\n\nfunc init() { b() }\n", "a()", "a2()", "function:i.go::init"},
		{"o.js", "const a = { run() { return 1; } };\nconst b = { run() { return 2; } };\n", "return 1", "return 9", "method:o.js::run"},
	}
	for _, c := range cases {
		evs := diffEvents(t, c.path, c.src, strings.Replace(c.src, c.from, c.to, 1))
		if !hasModified(evs, c.sig) {
			t.Errorf("%s: editing the first same-named declaration emitted %+v; want MODIFIED %s", c.path, evs, c.sig)
		}
	}
}

// Python indentation is syntax: moving a statement into a block is a change,
// re-indenting from 2 to 4 spaces is not.
func TestParse_PythonIndentationIsSemantic(t *testing.T) {
	before := "def f(x):\n    if x:\n        a()\n    return 1\n"
	moved := "def f(x):\n    if x:\n        a()\n        return 1\n"
	if evs := diffEvents(t, "f.py", before, moved); !hasModified(evs, "function:f.py::f") {
		t.Errorf("moving return into the if block emitted %+v; want MODIFIED", evs)
	}
	reindented := "def f(x):\n  if x:\n    a()\n  return 1\n"
	if evs := diffEvents(t, "f.py", before, reindented); len(evs) != 0 {
		t.Errorf("a 4->2 space re-indent emitted %+v; want none", evs)
	}
}

// An abstract class is a class: it and its methods are scoped and tracked.
func TestParse_TypeScriptAbstractClass(t *testing.T) {
	src := "abstract class Base { run() { return 1; } }\nabstract class Other { run() { return 2; } }\n"
	evs := diffEvents(t, "b.ts", src, strings.Replace(src, "return 1", "return 3", 1))
	if !hasModified(evs, "method:b.ts::Base.run") || !hasModified(evs, "class:b.ts::Base") {
		t.Errorf("edit inside an abstract class emitted %+v; want MODIFIED Base and Base.run", evs)
	}
}

// Decorators are part of the declaration they decorate.
func TestParse_PythonDecoratorEditIsAChange(t *testing.T) {
	src := "@app.route(\"/a\")\ndef view():\n    return 1\n"
	evs := diffEvents(t, "v.py", src, strings.Replace(src, "\"/a\"", "\"/admin\"", 1))
	if !hasModified(evs, "function:v.py::view") {
		t.Errorf("decorator edit emitted %+v; want MODIFIED", evs)
	}
}

// A line-ending conversion changes nothing, even inside multi-line strings.
func TestParse_CRLFConversionIsNotAChange(t *testing.T) {
	src := "package q\n\nfunc Q() string {\n\treturn `a\nb`\n}\n"
	if evs := diffEvents(t, "q.go", src, strings.ReplaceAll(src, "\n", "\r\n")); len(evs) != 0 {
		t.Errorf("LF->CRLF conversion emitted %+v; want none", evs)
	}
}
