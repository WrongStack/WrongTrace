package core

import (
	"encoding/json"
	"strings"
)

// Agent session discovery for editor-hosted agents (Antigravity, Cursor,
// Windsurf, Trae, Cline) used to count a session whenever its log merely
// CONTAINED the lowercased root path or even just the root's base name. A
// sibling checkout ("WrongTrace-docs") or any unrelated text mentioning the
// base name ("wrongtrace") leaked its sessions into this project -- the same
// class of defect already fixed for Claude Code. These helpers compare paths
// at component boundaries after normalizing the spellings those files use:
// backslashes (raw or JSON-escaped), percent-encoded file URIs
// ("file:///d%3A/Codebox/..."), and mixed case.

// normalizeMentionPath lowercases s, decodes %XX escapes, turns every
// backslash into '/', and collapses runs of '/'. It is applied identically to
// the root and to the searched content so both sides share one spelling.
func normalizeMentionPath(s string) string {
	s = strings.ToLower(percentDecodeLenient(s))
	var b strings.Builder
	b.Grow(len(s))
	prevSlash := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			c = '/'
		}
		if c == '/' {
			if prevSlash {
				continue
			}
			prevSlash = true
		} else {
			prevSlash = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// percentDecodeLenient decodes valid %XX sequences and leaves malformed ones
// untouched; url.PathUnescape rejects the whole string on one stray '%',
// which arbitrary transcript text routinely contains.
func percentDecodeLenient(s string) string {
	if strings.IndexByte(s, '%') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if hi, ok1 := hexVal(s[i+1]); ok1 {
				if lo, ok2 := hexVal(s[i+2]); ok2 {
					b.WriteByte(hi<<4 | lo)
					i += 2
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// pathNameByte reports whether c can continue a path component name, so a
// match that is followed or preceded by it is part of a DIFFERENT name
// ("api" inside "api-v2", "wrongtrace" inside "wrongtrace.old").
func pathNameByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '-' || c == '.' || c >= 0x80
}

// normalizedSessionRoot prepares a workspace root for mention matching,
// trimming any trailing separator.
func normalizedSessionRoot(root string) string {
	return strings.TrimRight(normalizeMentionPath(root), "/")
}

// contentMentionsRoot reports whether content references the root directory
// itself or a path beneath it, with component boundaries on both sides.
// normRoot must come from normalizedSessionRoot.
func contentMentionsRoot(content, normRoot string) bool {
	if normRoot == "" {
		return false
	}
	hay := normalizeMentionPath(content)
	for from := 0; from < len(hay); {
		idx := strings.Index(hay[from:], normRoot)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(normRoot)
		before := start == 0 || !pathNameByte(hay[start-1])
		after := end == len(hay) || !pathNameByte(hay[end])
		if before && after {
			return true
		}
		from = start + 1
	}
	return false
}

// fileURIToNormalizedPath converts a VS Code-style workspace URI into the
// normalized path spelling. Non-file URIs (vscode-remote://, ...) belong to
// another machine and never match a local root.
func fileURIToNormalizedPath(uri string) (string, bool) {
	u := strings.TrimSpace(uri)
	if len(u) < len("file://") || !strings.EqualFold(u[:len("file://")], "file://") {
		return "", false
	}
	p := normalizeMentionPath(u[len("file://"):])
	// "/d:/codebox" -> "d:/codebox" for Windows drive-letter URIs.
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' && p[1] >= 'a' && p[1] <= 'z' {
		p = p[1:]
	}
	return strings.TrimRight(p, "/"), true
}

// normalizedPathWithin reports equality or component-boundary containment.
func normalizedPathWithin(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

// workspaceJSONMatchesRoot decides whether a workspaceStorage workspace.json
// ({"folder": "file:///..."} or {"workspace": "file:///...code-workspace"})
// belongs to root. parsed is false when the file is not in that shape, so
// the caller can fall back to boundary-aware content matching.
func workspaceJSONMatchesRoot(data []byte, normRoot string) (matched, parsed bool) {
	var ws struct {
		Folder    string `json:"folder"`
		Workspace string `json:"workspace"`
	}
	if err := json.Unmarshal(data, &ws); err != nil || (ws.Folder == "" && ws.Workspace == "") {
		return false, false
	}
	for _, uri := range []string{ws.Folder, ws.Workspace} {
		if uri == "" {
			continue
		}
		if p, ok := fileURIToNormalizedPath(uri); ok && normRoot != "" && normalizedPathWithin(p, normRoot) {
			return true, true
		}
	}
	return false, true
}

// workspaceStorageMatches applies workspaceJSONMatchesRoot with a tolerant
// fallback for unrecognized shapes.
func workspaceStorageMatches(data []byte, normRoot string) bool {
	if matched, parsed := workspaceJSONMatchesRoot(data, normRoot); parsed {
		return matched
	}
	return contentMentionsRoot(string(data), normRoot)
}
