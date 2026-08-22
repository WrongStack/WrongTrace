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
	default:
		return "unknown"
	}
}

// DetectLanguage maps a file path's extension to a Tree-sitter language. It
// returns LangUnknown for unsupported files; the engine skips those entirely.
func DetectLanguage(path string) Language {
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.ToLower(filepath.Base(path))

	switch ext {
	case ".go":
		return LangGo
	case ".ts", ".tsx":
		return LangTypeScript
	case ".js", ".jsx", ".mjs", ".cjs":
		return LangJavaScript
	case ".py":
		return LangPython
	}

	// TypeScript declarations sometimes ship without an extension override.
	switch base {
	case "tsconfig.json", "package.json":
		// These are config, not source — skip.
		return LangUnknown
	}

	return LangUnknown
}
