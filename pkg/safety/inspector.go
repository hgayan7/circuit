// Package safety supplies conservative checks, not a sandbox or proof of safety.
package safety

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/hgayan7/circuit/pkg/config"
	"golang.org/x/text/unicode/norm"
)

const MaxInspectionBytes = 2 << 20

type Finding struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}
type Inspector struct{ cfg config.SafetyConfig }

func New(cfg config.SafetyConfig) *Inspector { return &Inspector{cfg: cfg} }
func (i *Inspector) InspectResponses() bool  { return i.cfg.PromptInjection }
func (i *Inspector) Enabled() bool           { return i.cfg.PromptInjection || i.cfg.Shell || i.cfg.SQL }

var injectionPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"instruction override", regexp.MustCompile(`(?is)\b(ignore|disregard|forget|override)\b.{0,80}\b(previous|prior|above|system|developer|safety)\b.{0,80}\b(instructions?|prompts?|rules?|polic(?:y|ies))\b`)},
	{"role spoofing", regexp.MustCompile(`(?i)(<\|(?:im_start|start_header_id)\|>\s*(?:system|developer)|\[INST\].*<<SYS>>|</?(?:system|developer)(?:\s[^>]*|)>|\b(?:system|developer)\s*(?:message|prompt)\s*:)`)},
	{"secret exfiltration instruction", regexp.MustCompile(`(?is)\b(send|upload|exfiltrate|transmit|post|reveal|print)\b.{0,120}\b(api[ _-]?keys?|secrets?|credentials?|passwords?|access[ _-]?tokens?|environment variables?|system prompt)\b`)},
	{"safety bypass instruction", regexp.MustCompile(`(?is)\b(disable|bypass|turn off)\b.{0,60}\b(guardrails?|safety checks?|approval checks?|security checks?)\b`)},
}
var encodedToken = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)

func normalized(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, norm.NFKC.String(text))
}
func detect(text string) *Finding {
	text = normalized(text)
	for _, p := range injectionPatterns {
		if p.re.MatchString(text) {
			return &Finding{"prompt_injection", "Possible prompt injection: " + p.name}
		}
	}
	return nil
}
func (i *Inspector) Text(text string) *Finding {
	if !i.cfg.PromptInjection {
		return nil
	}
	if len(text) > MaxInspectionBytes {
		return &Finding{"inspection_limit", "Text exceeds inspection limit"}
	}
	if f := detect(text); f != nil {
		return f
	}
	// Decode one layer only; bounded input avoids recursive decompression attacks.
	for _, token := range encodedToken.FindAllString(text, 32) {
		if b, err := base64.StdEncoding.DecodeString(token); err == nil {
			if f := detect(string(b)); f != nil {
				f.Reason += " (base64 encoded)"
				return f
			}
		}
	}
	return nil
}
func fieldMatches(key string, fields, defaults []string) bool {
	if len(fields) == 0 {
		fields = defaults
	}
	for _, f := range fields {
		if strings.EqualFold(key, f) {
			return true
		}
	}
	return false
}
func (i *Inspector) Arguments(args map[string]any) *Finding {
	var visit func(string, any, int) *Finding
	visit = func(key string, v any, depth int) *Finding {
		if depth > 64 {
			return &Finding{"inspection_limit", "Argument nesting exceeds inspection limit"}
		}
		executable := (i.cfg.Shell && fieldMatches(key, i.cfg.ShellFields, []string{"command", "cmd", "script"})) || (i.cfg.SQL && fieldMatches(key, i.cfg.SQLFields, []string{"sql", "query"}))
		if executable {
			if _, ok := v.(string); !ok {
				return &Finding{"invalid_arguments", fmt.Sprintf("Executable field %q must contain text", key)}
			}
		}
		switch x := v.(type) {
		case string:
			if len(x) > MaxInspectionBytes && i.Enabled() {
				return &Finding{"inspection_limit", "Argument exceeds inspection limit"}
			}
			if i.cfg.Shell && fieldMatches(key, i.cfg.ShellFields, []string{"command", "cmd", "script"}) {
				if err := CheckShell(x, i.cfg.AllowedCommands); err != nil {
					return &Finding{"shell", err.Error()}
				}
			}
			if i.cfg.SQL && fieldMatches(key, i.cfg.SQLFields, []string{"sql", "query"}) {
				if err := CheckSQL(x, i.cfg.AllowedSQLFunctions); err != nil {
					return &Finding{"sql", err.Error()}
				}
			}
			return i.Text(x)
		case map[string]any:
			for k, val := range x {
				if f := visit(k, val, depth+1); f != nil {
					return f
				}
			}
		case []any:
			for _, val := range x {
				if f := visit(key, val, depth+1); f != nil {
					return f
				}
			}
		default:
			if (i.cfg.Shell && fieldMatches(key, i.cfg.ShellFields, []string{"command", "cmd", "script"})) || (i.cfg.SQL && fieldMatches(key, i.cfg.SQLFields, []string{"sql", "query"})) {
				return &Finding{"invalid_arguments", fmt.Sprintf("Executable field %q must contain text", key)}
			}
		}
		return nil
	}
	return visit("", args, 0)
}

// ResponseValue walks decoded JSON, including escaped text inside tool results.
func (i *Inspector) ResponseValue(v any) *Finding {
	switch x := v.(type) {
	case string:
		return i.Text(x)
	case map[string]any:
		for _, val := range x {
			if f := i.ResponseValue(val); f != nil {
				return f
			}
		}
	case []any:
		for _, val := range x {
			if f := i.ResponseValue(val); f != nil {
				return f
			}
		}
	}
	return nil
}
