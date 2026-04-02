package translator

import (
	"fmt"
	"strings"
)

// RTSystemPrompt is the system message for the real-time translation session.
func RTSystemPrompt() string {
	return `You are an expert translator for GitHub Copilot AI internal reasoning text.
You receive the AI's thinking/reasoning text one turn at a time.
Translate the text faithfully and directly into the language and format specified in the user message.
Preserve the original meaning. Do not summarize or add commentary. No preamble. Output only the translation.`
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
		return "Format as a bullet-point list. Use - as the bullet character."
	case "numbered":
		return "Format as a numbered list (1. 2. 3. ...)."
	case "prose":
		return "Write as flowing prose paragraphs (no lists)."
	default:
		if format != "" {
			return format // custom instruction passed through verbatim
		}
		return "Format as a bullet-point list. Use - as the bullet character."
	}
}

// TranslateUserPrompt builds the user message for a real-time turn translation.
func TranslateUserPrompt(reasoningText, lang, format string) string {
	return fmt.Sprintf("Language: %s\nFormat: %s\nTask: Translate the following reasoning text directly into the target language. Do not summarize.\n\n%s",
		lang, FormatInstruction(format), reasoningText)
}

// SessionSummaryUserPrompt builds the user message for a per-session summary.
func SessionSummaryUserPrompt(label, reasoningText, lang, format string) string {
	return fmt.Sprintf(
		"Language: %s\nFormat: %s\nSession: %s\nTask: Summarize this coding session (5-8 items). Focus on what was accomplished, problems solved, and key technical decisions.\n\n%s",
		lang, FormatInstruction(format), label, reasoningText,
	)
}

// AllSessionsUserPrompt builds the user message for an all-sessions unified summary.
func AllSessionsUserPrompt(reasoningText, lang, format string) string {
	return fmt.Sprintf(
		"Language: %s\nFormat: %s\nTask: Create ONE unified summary (8-12 items) covering ALL sessions below. Do NOT split by session — provide a single integrated overview.\n\n%s",
		lang, FormatInstruction(format), reasoningText,
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
