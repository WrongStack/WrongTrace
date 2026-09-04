package proxy

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	// Secret patterns to prevent accidental leaks in LLM prompts
	awsKeyRe        = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	githubTokenRe   = regexp.MustCompile(`\b(?:ghp_[a-zA-Z0-9]{36}|github_pat_[a-zA-Z0-9_]{82})\b`)
	openAIKeyRe     = regexp.MustCompile(`\bsk-[a-zA-Z0-9_\-]{20,}\b`)
	anthropicKeyRe  = regexp.MustCompile(`\bsk-ant-[a-zA-Z0-9_\-]{20,}\b`)
	googleKeyRe     = regexp.MustCompile(`\bAIzaSy[a-zA-Z0-9_\-]{33}\b`)
	privateKeyRe    = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[a-zA-Z0-9/+= \r\n]+-----END [A-Z ]*PRIVATE KEY-----`)
	dbURLRe         = regexp.MustCompile(`\b((?:postgres|postgresql|mysql|mongodb|redis)://[^:\s]+):([^@\s]+)@`)
	genericSecretRe = regexp.MustCompile(`(?i)\b(?:password|passwd|api_secret|client_secret|auth_token)\s*[:=]\s*["']?([a-zA-Z0-9!@#$%^&*()_+\-={}\[\]]{8,})["']?`)
)

var secretFoldTargets = []struct {
	lower      []byte
	firstLower byte
	firstUpper byte
}{
	{lower: []byte("password"), firstLower: 'p', firstUpper: 'P'},
	{lower: []byte("passwd"), firstLower: 'p', firstUpper: 'P'},
	{lower: []byte("api_secret"), firstLower: 'a', firstUpper: 'A'},
	{lower: []byte("client_secret"), firstLower: 'c', firstUpper: 'C'},
	{lower: []byte("auth_token"), firstLower: 'a', firstUpper: 'A'},
}

func bytesContainsFoldStatic(b, subLower []byte, firstLower, firstUpper byte) bool {
	subLen := len(subLower)
	if len(b) < subLen {
		return false
	}
	maxIdx := len(b) - subLen
	for i := 0; i <= maxIdx; i++ {
		c := b[i]
		if c == firstLower || c == firstUpper {
			match := true
			for j := 1; j < subLen; j++ {
				bc := b[i+j]
				if bc >= 'A' && bc <= 'Z' {
					bc += 32
				}
				if bc != subLower[j] {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

func hasGenericSecret(b []byte) bool {
	for _, target := range secretFoldTargets {
		if bytesContainsFoldStatic(b, target.lower, target.firstLower, target.firstUpper) {
			return true
		}
	}
	return false
}

var (
	bPrivKey    = []byte("PRIVATE KEY")
	bAKIA       = []byte("AKIA")
	bGhp        = []byte("ghp_")
	bGithubPat  = []byte("github_pat_")
	bSkAnt      = []byte("sk-ant-")
	bSk         = []byte("sk-")
	bAIzaSy     = []byte("AIzaSy")
	bColonSlash = []byte("://")
	bPostgres   = []byte("postgres")
	bMysql      = []byte("mysql")
	bMongo      = []byte("mongodb")
	bRedis      = []byte("redis")
)

// genericSecretPlaceholder replaces a matched secret value in place.
const genericSecretPlaceholder = "[REDACTED_SECRET]"

// ScanAndRedactSecrets inspects request payloads for confidential secrets and masks them before sending to LLMs.
// Uses fast-path substring checks so multi-megabyte payloads bypass expensive regex scans when no matching tokens exist.
func ScanAndRedactSecrets(body []byte) ([]byte, int) {
	if len(body) < 16 {
		return body, 0
	}

	hasPrivateKey := bytes.Contains(body, bPrivKey)
	hasAWS := bytes.Contains(body, bAKIA)
	hasGitHub := bytes.Contains(body, bGhp) || bytes.Contains(body, bGithubPat)
	hasAnthropic := bytes.Contains(body, bSkAnt)
	hasOpenAI := bytes.Contains(body, bSk)
	hasGoogle := bytes.Contains(body, bAIzaSy)
	hasDB := bytes.Contains(body, bColonSlash) && (bytes.Contains(body, bPostgres) || bytes.Contains(body, bMysql) || bytes.Contains(body, bMongo) || bytes.Contains(body, bRedis))
	hasGeneric := hasGenericSecret(body)

	if !hasPrivateKey && !hasAWS && !hasGitHub && !hasAnthropic && !hasOpenAI && !hasGoogle && !hasDB && !hasGeneric {
		return body, 0
	}

	text := string(body)
	redactionCount := 0

	// 1. Redact Private Keys
	if hasPrivateKey && privateKeyRe.MatchString(text) {
		text = privateKeyRe.ReplaceAllString(text, "[REDACTED_PRIVATE_KEY]")
		redactionCount++
	}

	// 2. Redact AWS Access Keys
	if hasAWS && awsKeyRe.MatchString(text) {
		text = awsKeyRe.ReplaceAllString(text, "[REDACTED_AWS_KEY]")
		redactionCount++
	}

	// 3. Redact GitHub Tokens
	if hasGitHub && githubTokenRe.MatchString(text) {
		text = githubTokenRe.ReplaceAllString(text, "[REDACTED_GITHUB_TOKEN]")
		redactionCount++
	}

	// 4. Redact OpenAI / Anthropic / Google Keys inside prompts
	if hasAnthropic && anthropicKeyRe.MatchString(text) {
		text = anthropicKeyRe.ReplaceAllString(text, "[REDACTED_ANTHROPIC_KEY]")
		redactionCount++
	}
	if hasOpenAI && openAIKeyRe.MatchString(text) {
		text = openAIKeyRe.ReplaceAllString(text, "[REDACTED_OPENAI_KEY]")
		redactionCount++
	}
	if hasGoogle && googleKeyRe.MatchString(text) {
		text = googleKeyRe.ReplaceAllString(text, "[REDACTED_GOOGLE_KEY]")
		redactionCount++
	}

	// 5. Redact DB Passwords
	if hasDB && dbURLRe.MatchString(text) {
		text = dbURLRe.ReplaceAllString(text, "${1}:[REDACTED_DB_PASSWORD]@")
		redactionCount++
	}

	// 6. Redact Generic Secrets.
	//
	// Only capture group 1 -- the secret value itself -- is rewritten. The
	// pattern deliberately consumes the surrounding delimiters (\s*[:=]\s* and
	// an optional quote on each side) in order to FIND the value, but replacing
	// the whole match threw those delimiters away. For a secret pasted into a
	// prompt that meant the JSON string's own closing quote was eaten:
	//
	//   {"content":"PASSWORD=hunter2secret"}
	//     -> {"content":"PASSWORD= [REDACTED_SECRET]}
	//
	// The gateway forwards that to the provider, which rejects it as malformed.
	// Redaction must not be able to break the request it is protecting.
	if hasGeneric {
		if locs := genericSecretRe.FindAllStringSubmatchIndex(text, -1); len(locs) > 0 {
			var b strings.Builder
			b.Grow(len(text))
			prev, changed := 0, false
			for _, loc := range locs {
				start, end := loc[2], loc[3] // capture group 1: the secret value
				if start < 0 {
					continue
				}
				// Already-redacted text still matches the pattern (the placeholder
				// is bracketed and the class admits brackets), so skip it rather
				// than rewrite and re-count it on a second pass.
				if text[start:end] == genericSecretPlaceholder {
					continue
				}
				b.WriteString(text[prev:start])
				b.WriteString(genericSecretPlaceholder)
				prev = end
				changed = true
				redactionCount++
			}
			if changed {
				b.WriteString(text[prev:])
				text = b.String()
			}
		}
	}

	if redactionCount > 0 {
		return []byte(text), redactionCount
	}
	return body, 0
}
