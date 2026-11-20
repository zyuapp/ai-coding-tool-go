package protocol

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// stderrKeep is how much of stderr an exit error carries, in characters.
const stderrKeep = 2048

type redaction struct {
	pattern     string
	ignoreCase  bool
	replacement string
}

// redactions are the credentials the SDK takes out of stderr before an error carries it, in its order.
var redactions = []redaction{
	{`\bBearer\s+(?=[A-Za-z0-9._~+/=-](?:[A-Za-z0-9._~+/=-]|%[0-9A-Fa-f]{2}){7})[A-Za-z0-9._~+/=-](?:[A-Za-z0-9._~+/=-]+|%[0-9A-Fa-f]{2})*`, true, "Bearer <token>"},
	{`(:\s*)Basic\s+[A-Za-z0-9+/=]{8,}`, true, "$1Basic <token>"},
	{`(?:\bsk-ant-[A-Za-z0-9_-]{8,}|sk-ant-(?=[A-Za-z0-9_-]{0,64}?(?=[A-Za-z0-9]{8})(?=[A-Za-z0-9]{0,64}[0-9])(?=[A-Za-z0-9]{0,64}[A-Za-z]))[A-Za-z0-9_-]{8,})`, false, "<token>"},
	{`(?:\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}|sk-(?:proj|svcacct|admin)-(?=[A-Za-z0-9_-]{0,64}?(?=[A-Za-z0-9]{20})(?=[A-Za-z0-9]{0,64}[0-9])(?=[A-Za-z0-9]{0,64}[A-Za-z]))[A-Za-z0-9_-]{20,})`, false, "<token>"},
	{`\bsk-[A-Za-z0-9_-]{20,}`, false, "<token>"},
	{`(^|[^A-Za-z0-9+])(?:AKIA|ASIA|ABIA|ACCA|A3T[A-Z0-9])[A-Z0-9]{16}(?![A-Za-z0-9+=])`, false, "$1<token>"},
	{`(?:\bgh[opusr]_[A-Za-z0-9]{20,}|gh[opusr]_(?=[A-Za-z0-9]{0,64}[0-9])[A-Za-z0-9]{20,})`, false, "<token>"},
	{`github_pat_[A-Za-z0-9_]{82,}`, false, "<token>"},
	{`sq0(?:atp|csp)-[A-Za-z0-9_-]{22,}`, false, "<token>"},
	{`(^|[^A-Za-z0-9+/])EAAA[A-Za-z0-9+/=%_-]{56,}`, false, "$1<token>"},
	{`(?:\bgl(?:pat|dt|rt|ft|soat|oas|agent|ptt|cbt|imt|ffct)-[A-Za-z0-9_=-]{20,}(?:\.[0-9a-z]{9})?|gl(?:pat|dt|rt|ft|soat|oas|agent|ptt|cbt|imt|ffct)-(?=[A-Za-z0-9_=-]{0,64}[0-9])[A-Za-z0-9_=-]{20,}(?:\.[0-9a-z]{9})?)`, false, "<token>"},
	{`(?:\b(?:xox[abe-z](?:\.xox[a-z])?|xapp|xwfp)-(?=[A-Za-z0-9+/=%_-]{0,64}[0-9])[A-Za-z0-9+/=%_-]{10,}|(?:xox[abe-z](?:\.xox[a-z])?|xapp|xwfp)-(?=[0-9])[A-Za-z0-9+/=%_-]{10,})`, true, "<token>"},
	{`(?:\bxox[cd]-[A-Za-z0-9+/=%_-]{10,}|xox[cd]-(?=[A-Za-z0-9+/=%_-]{0,64}[0-9])[A-Za-z0-9+/=%_-]{16,})`, true, "<token>"},
	{`hooks\.slack\.com\/(?:services|workflows|triggers)\/[A-Za-z0-9/_-]+`, true, "hooks.slack.com/<redacted>"},
	{`(?<![A-Za-z0-9_-])(?=[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{10}[A-Za-z0-9_-]*\.[A-Za-z0-9_-]{10}[A-Za-z0-9_-]*)((?:[A-Za-z0-9_-]*?-)??)ey[A-Za-z0-9_-]{10}[A-Za-z0-9_-]*\.[A-Za-z0-9_-]{10}[A-Za-z0-9_-]*\.[A-Za-z0-9_-]{10}[A-Za-z0-9_-]*`, false, "$1<jwt>"},
	{`(^|[^A-Za-z0-9+_-])AIza[A-Za-z0-9_-]{35}(?![A-Za-z0-9+=_-])`, false, "$1<token>"},
	{`(^|[^A-Za-z0-9+_-])GOCSPX-[A-Za-z0-9_-]{28}(?![A-Za-z0-9+=_-])`, false, "$1<token>"},
	{`(^|[^A-Za-z0-9+_-])[sr]k_(?:live|test|prod)_(?=[A-Za-z0-9]{0,64}[0-9])[A-Za-z0-9]{24,}(?![A-Za-z0-9+=_-])`, false, "$1<token>"},
}

var compiledRedactions = func() []*regexp2.Regexp {
	compiled := make([]*regexp2.Regexp, len(redactions))
	for index, redaction := range redactions {
		options := regexp2.RegexOptions(regexp2.ECMAScript)
		if redaction.ignoreCase {
			options |= regexp2.IgnoreCase
		}
		compiled[index] = regexp2.MustCompile(redaction.pattern, options)
	}
	return compiled
}()

// redact replaces credentials in text with placeholders.
func redact(text string) string {
	for index, pattern := range compiledRedactions {
		if replaced, err := pattern.Replace(text, redactions[index].replacement, -1, -1); err == nil {
			text = replaced
		}
	}
	return text
}

// lastCharacters keeps the last n characters of text.
func lastCharacters(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[len(runes)-n:])
}

// stderrTail is the stderr an exit error carries: credentials replaced, its last characters, trimmed.
func stderrTail(text string) string {
	return strings.TrimSpace(lastCharacters(redact(text), stderrKeep))
}
