package proxy

import (
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

// ScanAndRedactSecrets inspects request payloads for confidential secrets and masks them before sending to LLMs.
func ScanAndRedactSecrets(body []byte) ([]byte, int) {
	if len(body) == 0 {
		return body, 0
	}

	text := string(body)
	redactionCount := 0

	// 1. Redact Private Keys
	if privateKeyRe.MatchString(text) {
		text = privateKeyRe.ReplaceAllString(text, "[REDACTED_PRIVATE_KEY]")
		redactionCount++
	}

	// 2. Redact AWS Access Keys
	if awsKeyRe.MatchString(text) {
		text = awsKeyRe.ReplaceAllString(text, "[REDACTED_AWS_KEY]")
		redactionCount++
	}

	// 3. Redact GitHub Tokens
	if githubTokenRe.MatchString(text) {
		text = githubTokenRe.ReplaceAllString(text, "[REDACTED_GITHUB_TOKEN]")
		redactionCount++
	}

	// 4. Redact OpenAI / Anthropic / Google Keys inside prompts
	if anthropicKeyRe.MatchString(text) {
		text = anthropicKeyRe.ReplaceAllString(text, "[REDACTED_ANTHROPIC_KEY]")
		redactionCount++
	}
	if openAIKeyRe.MatchString(text) {
		text = openAIKeyRe.ReplaceAllString(text, "[REDACTED_OPENAI_KEY]")
		redactionCount++
	}
	if googleKeyRe.MatchString(text) {
		text = googleKeyRe.ReplaceAllString(text, "[REDACTED_GOOGLE_KEY]")
		redactionCount++
	}

	// 5. Redact DB Passwords
	if dbURLRe.MatchString(text) {
		text = dbURLRe.ReplaceAllString(text, "${1}:[REDACTED_DB_PASSWORD]@")
		redactionCount++
	}

	// 6. Redact Generic Secrets
	if genericSecretRe.MatchString(text) {
		text = genericSecretRe.ReplaceAllStringFunc(text, func(m string) string {
			redactionCount++
			if idx := strings.IndexAny(m, ":="); idx != -1 {
				return m[:idx+1] + " [REDACTED_SECRET]"
			}
			return "[REDACTED_SECRET]"
		})
	}

	if redactionCount > 0 {
		return []byte(text), redactionCount
	}
	return body, 0
}
