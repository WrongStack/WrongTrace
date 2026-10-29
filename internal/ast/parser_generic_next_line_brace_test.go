package ast

import (
	"strings"
	"testing"
)

// Brace style is a project choice, not a language feature. Microsoft's own C#
// convention, PHP PSR-12 class bodies and a good deal of Java and Dart put the
// declaration's opening '{' on the line AFTER the head:
//
//	public class Calc
//	{
//
// parseGenericSource treated such a head as brace-less. Two consequences, both
// silent, and both fixed by starting the body scan at the line that actually
// carries '{' (braceLine):
//
//  1. typedDeclName's cheap reject requires the head to END in '{', so the
//     method produced NO node at all: "function:Calc.cs::Add is absent from the
//     snapshot", so no ADDED/MODIFIED/DELETED event could ever be attributed
//     to it.
//  2. the class node was created but its span stopped on the head line
//     (3..3, loc=1), freezing the body hash — and Diff decides MODIFIED by
//     comparing BodyHash, so no edit anywhere inside the class body emitted an
//     event either.
//
// Measured pre-fix, both fixtures: signatures = [class:Calc.cs::Calc] only,
// class span 3..3, events = none. The SAME-LINE spelling of the same logical
// file was fully tracked (method 4..6, class 3..7, MODIFIED emitted), which is
// what makes this a style-blindness bug rather than a language gap.
//
// Case (1) is checked against the recogniser's own guards in
// TestTypedDeclNameWithBraceDoesNotInventDeclarations; this file pins the
// observable parser and Diff behaviour.

const nextLineBraceCS = `using System;

public class Calc
{
    public int Add(int a, int b)
    {
        return a + b;
    }
}
`

const sameLineBraceCS = `using System;

public class Calc {
    public int Add(int a, int b) {
        return a + b;
    }
}
`

const nextLineBraceJava = `package p;

public class Calc
{
    public int add(int a, int b)
    {
        return a + b;
    }
}
`

const sameLineBraceJava = `package p;

public class Calc {
    public int add(int a, int b) {
        return a + b;
    }
}
`

const nextLineBracePHP = `class Svc
{
    public function hello()
    {
        return 1;
    }
}
`

// TestParseGenericSource_NextLineBraceSpansBody pins that a declaration whose
// '{' is on the following line spans its body exactly as the same-line spelling
// does. This is the assertion that keeps the body hash live.
func TestParseGenericSource_NextLineBraceSpansBody(t *testing.T) {
	cases := []struct {
		name string
		path string
		src  string
		lang Language
		want map[string][2]uint32
	}{
		{
			name: "csharp next-line brace",
			path: "Calc.cs",
			src:  nextLineBraceCS,
			lang: LangCSharp,
			want: map[string][2]uint32{
				"class:Calc.cs::Calc":   {3, 9},
				"function:Calc.cs::Add": {5, 8},
			},
		},
		{
			name: "csharp same-line brace (control)",
			path: "Calc.cs",
			src:  sameLineBraceCS,
			lang: LangCSharp,
			want: map[string][2]uint32{
				"class:Calc.cs::Calc":   {3, 7},
				"function:Calc.cs::Add": {4, 6},
			},
		},
		{
			name: "java next-line brace",
			path: "Calc.java",
			src:  nextLineBraceJava,
			lang: LangJava,
			want: map[string][2]uint32{
				"class:Calc.java::Calc":   {3, 9},
				"function:Calc.java::add": {5, 8},
			},
		},
		{
			name: "java same-line brace (control)",
			path: "Calc.java",
			src:  sameLineBraceJava,
			lang: LangJava,
			want: map[string][2]uint32{
				"class:Calc.java::Calc":   {3, 7},
				"function:Calc.java::add": {4, 6},
			},
		},
		{
			name: "php PSR-12 class with next-line braces",
			path: "Svc.php",
			src:  nextLineBracePHP,
			lang: LangPHP,
			want: map[string][2]uint32{
				"class:Svc.php::Svc":      {1, 7},
				"function:Svc.php::hello": {3, 6},
			},
		},
	}

	for _, tc := range cases {
		snap := parseGenericSource(tc.path, []byte(tc.src), tc.lang)
		for sig, want := range tc.want {
			n, ok := snap.Nodes[sig]
			if !ok {
				t.Errorf("%s: missing node %q; got %v", tc.name, sig, sortedSigsOf(snap))
				continue
			}
			if n.StartLine != want[0] || n.EndLine != want[1] {
				t.Errorf("%s: %s spans %d..%d, want %d..%d",
					tc.name, sig, n.StartLine, n.EndLine, want[0], want[1])
			}
		}
		for sig := range tc.want {
			if snap.Nodes[sig].Hash == "" {
				t.Errorf("%s: node %q has an empty body hash", tc.name, sig)
			}
		}
	}
}

// TestTypedDeclNameWithBraceDoesNotInventDeclarations pins the safety property
// the braceOnNextLine flag must not break. The flag only supplies the missing
// '{' — every other guard still has to reject the line, so a continuation line
// can never turn a statement head into a declaration. Without that, dropping
// the brace test outright would have made `x = compute(a)` declare `compute`
// (it has a return-type-shaped head and a parameter list) and
// `var list = new List<int>()` declare `List`.
func TestTypedDeclNameWithBraceDoesNotInventDeclarations(t *testing.T) {
	for _, line := range []string{
		"if (n > 0)",
		"while (running)",
		"for (int i = 0; i < n; i++)",
		"switch (mode)",
		"catch (Exception e)",
		"using (var s = Open())",
		"lock (gate)",
		"synchronized (this)",
		"else if (x)",
		"return new Foo(x)",
		"var list = new List<int>()",
		"x = compute(a)",
		"foo(bar)",
		"do",
		"try",
	} {
		if got := typedDeclNameWithBrace(line, true); got != "" {
			t.Errorf("typedDeclNameWithBrace(%q, true) = %q, want no declaration", line, got)
		}
	}

	// The same heads stay undeclared in the same-line spelling, so the flag is
	// not papering over a wider regression.
	for _, line := range []string{
		"if (n > 0) {",
		"while (running) {",
		"var list = new List<int>() {",
	} {
		if got := typedDeclName(line); got != "" {
			t.Errorf("typedDeclName(%q) = %q, want no declaration", line, got)
		}
	}

	// The head the flag exists for is still recognised, and the flag alone is
	// not enough: the head must still carry a parameter list.
	if got := typedDeclNameWithBrace("public int Add(int a, int b)", true); got != "Add" {
		t.Errorf("typedDeclNameWithBrace(head, true) = %q, want %q", got, "Add")
	}
	if got := typedDeclNameWithBrace("public int Counter", true); got != "" {
		t.Errorf("typedDeclNameWithBrace(%q, true) = %q, want no declaration (no parameter list)",
			"public int Counter", got)
	}
}

// TestParseGenericSource_NextLineBraceBodyEditEmitsDiffEvent is the end-to-end
// consequence: an edit inside a method body written in the next-line-brace
// style must be attributed to that method, exactly as the same-line spelling
// already was.
func TestParseGenericSource_NextLineBraceBodyEditEmitsDiffEvent(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		src    string
		lang   Language
		symbol string
		// A real code change, not a comment: body hashing normalizes comments
		// and whitespace away, so only changed code moves the hash.
		from, to string
	}{
		{"csharp", "Calc.cs", nextLineBraceCS, LangCSharp, "function:Calc.cs::Add", "a + b", "a * b"},
		{"java", "Calc.java", nextLineBraceJava, LangJava, "function:Calc.java::add", "a + b", "a * b"},
		{"php", "Svc.php", nextLineBracePHP, LangPHP, "function:Svc.php::hello", "return 1;", "return 2;"},
	}

	for _, tc := range cases {
		before := parseGenericSource(tc.path, []byte(tc.src), tc.lang)
		edited := strings.Replace(tc.src, tc.from, tc.to, 1)
		if edited == tc.src {
			t.Fatalf("%s: fixture edit did not apply", tc.name)
		}
		after := parseGenericSource(tc.path, []byte(edited), tc.lang)

		var got []string
		modified := false
		for _, ev := range Diff("p", before, after).Events {
			got = append(got, string(ev.Action)+" "+ev.Signature)
			if ev.Action == ActionModified && ev.Signature == tc.symbol {
				modified = true
			}
		}
		if !modified {
			t.Errorf("%s: an edit inside %s (next-line brace) emitted no MODIFIED event; got %v",
				tc.name, tc.symbol, got)
		}
	}
}

// TestParseGenericSource_NextLineBraceKeepsRubyEndScan is the exclusion
// control: Ruby closes blocks with `end`, so a later line that opens with '{'
// is a hash literal, not a body. Ruby's span must still come from the `end`
// scan — if the next-line-brace rule ever stops excluding it, the method's span
// would end at the hash literal.
func TestParseGenericSource_NextLineBraceKeepsRubyEndScan(t *testing.T) {
	snap := parseGenericSource("Build.rb", []byte(
		"def build\n  { a: 1 }\nend\n"), LangRuby)
	n, ok := snap.Nodes["function:Build.rb::build"]
	if !ok {
		t.Fatalf("missing node function:Build.rb::build; got %v", sortedSigsOf(snap))
	}
	if n.StartLine != 1 || n.EndLine != 3 {
		t.Errorf("ruby def spans %d..%d, want 1..3 (a later '{' must not be taken as the body)",
			n.StartLine, n.EndLine)
	}
}

// TestParseGenericSource_NextLineBraceSpanDoesNotRunAway guards the other end of
// the change: a head followed by lines that never close must not swallow the
// rest of the file, and a later declaration in the same file must still be seen.
func TestParseGenericSource_NextLineBraceSpanDoesNotRunAway(t *testing.T) {
	snap := parseGenericSource("Open.cs", []byte(
		"public class Open\n"+
			"{\n"+
			"    public int Add(int a, int b)\n"+
			"    {\n"+
			"        return a + b;"), LangCSharp)
	n, ok := snap.Nodes["class:Open.cs::Open"]
	if !ok {
		t.Fatalf("missing node class:Open.cs::Open; got %v", sortedSigsOf(snap))
	}
	if n.EndLine != 5 {
		t.Errorf("unclosed class spans to line %d, want 5 (it must stop at EOF, not run on)", n.EndLine)
	}

	later := parseGenericSource("Later.cs", []byte(
		"public class First\n"+
			"{\n"+
			"}\n"+
			"\n"+
			"public class Second\n"+
			"{\n"+
			"}\n"), LangCSharp)
	for _, sig := range []string{"class:Later.cs::First", "class:Later.cs::Second"} {
		if _, ok := later.Nodes[sig]; !ok {
			t.Errorf("a later declaration was swallowed; missing %q in %v", sig, sortedSigsOf(later))
		}
	}
}
