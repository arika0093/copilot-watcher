package translator

import (
	"fmt"
	"regexp"
	"strings"
)

// xmlTagRe matches complete XML/HTML-like tag block pairs (e.g. <reminder>…</reminder>).
var xmlTagRe = regexp.MustCompile(`(?s)<[a-zA-Z][^>]*>.*?</[a-zA-Z][^>]*>`)

// orphanTagRe matches leftover opening or closing tags after pair stripping.
var orphanTagRe = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9_:-]*(?:\s[^>]*)?>`)

// StripXMLTags removes XML/HTML-like blocks (e.g. <reminder>…</reminder>,
// <sql_tables>…</sql_tables>) that appear as internal directives in Copilot CLI
// reasoning/context text. Iterates to handle nested structures, then strips orphans.
func StripXMLTags(text string) string {
	s := text
	for {
		ns := xmlTagRe.ReplaceAllString(s, "")
		if ns == s {
			break
		}
		s = ns
	}
	s = orphanTagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// RTSystemPrompt is the system message for the real-time translation session.
func RTSystemPrompt() string {
	return `You are an expert translator for GitHub Copilot AI internal reasoning text.
You receive the AI's thinking/reasoning text one turn at a time.
Translate the text faithfully into the language and format specified in the user message.
Preserve the original meaning. No preamble. Output only the translation.`
}

// HistSystemPrompt is the system message for the history summary session.
func HistSystemPrompt() string {
	return `You are an expert at summarizing GitHub Copilot AI coding sessions.
You receive reasoning/thinking text from coding sessions.
Respond ONLY with summaries in the language and format specified in the user message.
No preamble. No explanation. Output only the summary.`
}

// FormatInstruction returns the AI prompt fragment for the given format code.
// Custom formats pass through the raw string directly.
func FormatInstruction(format string) string {
	switch format {
	case "bullets":
		return "Summarize as a concise bullet list (3–8 items). Start each bullet line with '- '. Do NOT add '- ' to regular sentences."
	case "translate-only":
		return "Translate faithfully and directly. Do not summarize or condense. Preserve all details and structure from the original."
	case "conversational":
		return "Summarize in casual, conversational language. Use natural flowing sentences. No bullet points or lists. Break the text into short paragraphs (separated by blank lines) for readability — one paragraph per main idea."
	default:
		if format != "" {
			return format
		}
		return "Summarize in casual, conversational language. Use natural flowing sentences. No bullet points or lists. Break the text into short paragraphs (separated by blank lines) for readability — one paragraph per main idea."
	}
}

// TranslateUserPrompt builds the user message for a real-time turn translation.
func TranslateUserPrompt(reasoningText, lang, format string) string {
	return fmt.Sprintf("Language: %s\nFormat: %s\nTask: Translate the following reasoning text into the target language.\n\n%s",
		lang, FormatInstruction(format), StripXMLTags(reasoningText))
}

// SessionSummaryUserPrompt builds the user message for a per-session summary.
func SessionSummaryUserPrompt(label, reasoningText, lang, format string) string {
	return fmt.Sprintf(
		"Language: %s\nFormat: %s\nSession: %s\nTask: Summarize this request. Focus on: (1) what problem or goal was presented, and (2) what was actually done or decided to address it. Be concise.\n\n%s",
		lang, FormatInstruction(format), label, StripXMLTags(reasoningText),
	)
}

// RequestSummaryUserPrompt builds the summary prompt for a single request turn.
// Combines user question, AI reasoning, and AI response for a complete picture.
func RequestSummaryUserPrompt(userMsg, reasoning, response, lang, format string) string {
	var sb strings.Builder
	if userMsg != "" {
		sb.WriteString("User request:\n")
		sb.WriteString(StripXMLTags(userMsg))
		sb.WriteString("\n\n")
	}
	if reasoning != "" {
		sb.WriteString("AI internal reasoning:\n")
		sb.WriteString(StripXMLTags(reasoning))
		sb.WriteString("\n\n")
	}
	if response != "" {
		sb.WriteString("AI response to user:\n")
		sb.WriteString(StripXMLTags(response))
	}
	return fmt.Sprintf(
		"Language: %s\nFormat: %s\nTask: Summarize this Copilot CLI request. Focus on: (1) what problem or goal the user presented, (2) what the AI thought and decided, (3) what outcome or response was given. Be concise.\n\n%s",
		lang, FormatInstruction(format), strings.TrimSpace(sb.String()),
	)
}
func AllSessionsUserPrompt(reasoningText, lang, format string) string {
	return fmt.Sprintf(
		"Language: %s\nFormat: %s\nTask: Create ONE unified summary across ALL sessions below. For each notable request, describe: (1) the problem or goal, and (2) what was done. Do NOT split by session — integrate everything into a single cohesive overview.\n\n%s",
		lang, FormatInstruction(format), StripXMLTags(reasoningText),
	)
}

// IsJapaneseLike returns true when the language name implies CJK output
// (used for dialect variants without exact-matching on a fixed list).
func IsJapaneseLike(lang string) bool {
	lower := strings.ToLower(lang)
	return strings.Contains(lower, "japanese") ||
		strings.Contains(lower, "chinese") ||
		strings.Contains(lower, "korean") ||
		strings.Contains(lang, "日本") ||
		strings.Contains(lang, "中文") ||
		strings.Contains(lang, "한국")
}
