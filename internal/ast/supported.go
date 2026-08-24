// Package ast provides multi-language Tree-sitter parsing and semantic diffing
// for the WrongTrace observer. It walks function/method/class declarations,
// builds normalized signatures, computes SHA256 hashes of the node bodies, and
// emits ADD/MOD/DEL events that the core engine persists to the database.
package ast

import (
	"path/filepath"
	"strings"
)

// Language identifies a Tree-sitter language binding.
type Language int

const (
	LangUnknown Language = iota
	LangGo
	LangTypeScript
	LangJavaScript
	LangPython
	LangRust
	LangCpp
	LangJava
	LangCSharp
	LangPHP
	LangRuby
)

// String returns a short, lower-case identifier suitable for DB columns.
func (l Language) String() string {
	switch l {
	case LangGo:
		return "go"
	case LangTypeScript:
		return "typescript"
	case LangJavaScript:
		return "javascript"
	case LangPython:
		return "python"
	case LangRust:
		return "rust"
	case LangCpp:
		return "cpp"
	case LangJava:
		return "java"
	case LangCSharp:
		return "csharp"
	case LangPHP:
		return "php"
	case LangRuby:
		return "ruby"
	default:
		return "unknown"
	}
}

// DetectLanguage maps a file path's extension to a Tree-sitter or semantic language.
func DetectLanguage(path string) Language {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".go":
		return LangGo
	case ".ts", ".tsx":
		return LangTypeScript
	case ".js", ".jsx", ".mjs", ".cjs":
		return LangJavaScript
	case ".py":
		return LangPython
	case ".rs":
		return LangRust
	case ".c", ".h", ".cpp", ".hpp", ".cc", ".cxx":
		return LangCpp
	case ".java":
		return LangJava
	case ".cs":
		return LangCSharp
	case ".php":
		return LangPHP
	case ".rb":
		return LangRuby
	}

	// Config files to skip (evaluated lazily only when extension didn't match).
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "tsconfig.json", "package.json", "go.mod", "go.sum", "cargo.lock":
		return LangUnknown
	}

	return LangUnknown
}

