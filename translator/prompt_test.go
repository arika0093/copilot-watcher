package translator

import (
	"strings"
	"testing"
)

func TestStripXMLTags(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "removes nested tags",
			text: "visible<outer>hide<inner>more</inner></outer>",
			want: "visible",
		},
		{
			name: "removes orphan tags",
			text: "before</orphan><tag>after",
			want: "beforeafter",
		},
		{
			name: "leaves plain text",
			text: "plain text",
			want: "plain text",
		},
	}

	for _, tt := range tests {
		got := StripXMLTags(tt.text)
		if got != tt.want {
			t.Fatalf("%s: StripXMLTags() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestFormatInstruction(t *testing.T) {
	tests := []struct {
		name   string
		format string
		want   string
	}{
		{
			name:   "known format",
			format: "bullets",
			want:   "Summarize as a concise bullet list (3-8 items). Start each bullet line with '- '. Do NOT add '- ' to regular sentences.",
		},
		{
			name:   "custom format",
			format: "Keep it terse",
			want:   "Keep it terse",
		},
		{
			name:   "default format",
			format: "",
			want:   "Summarize in casual, conversational language. Use natural flowing sentences. No bullet points or lists. Break the text into short paragraphs (separated by blank lines) for readability - one paragraph per main idea.",
		},
	}

	for _, tt := range tests {
		got := strings.ReplaceAll(FormatInstruction(tt.format), "–", "-")
		got = strings.ReplaceAll(got, "—", "-")
		if got != tt.want {
			t.Fatalf("%s: FormatInstruction() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTranslateUserPromptStripsXMLAndIncludesMetadata(t *testing.T) {
	prompt := TranslateUserPrompt("visible<reminder>hidden</reminder>", "Japanese", "bullets")

	if !strings.Contains(prompt, "Language: Japanese") {
		t.Fatalf("prompt missing language metadata: %q", prompt)
	}
	if !strings.Contains(prompt, "Summarize as a concise bullet list") {
		t.Fatalf("prompt missing format instruction: %q", prompt)
	}
	if !strings.Contains(prompt, "visible") {
		t.Fatalf("prompt missing visible text: %q", prompt)
	}
	if strings.Contains(prompt, "hidden") || strings.Contains(prompt, "reminder") {
		t.Fatalf("prompt still contains stripped XML content: %q", prompt)
	}
}

func TestLiveRequestUserPromptIncludesAllAvailableSections(t *testing.T) {
	prompt := LiveRequestUserPrompt("ask<hidden>x</hidden>", "think<hidden>y</hidden>", "reply", "Japanese", "translate-only")

	if !strings.Contains(prompt, "User request:\nask") {
		t.Fatalf("prompt missing user section: %q", prompt)
	}
	if !strings.Contains(prompt, "AI internal reasoning:\nthink") {
		t.Fatalf("prompt missing reasoning section: %q", prompt)
	}
	if !strings.Contains(prompt, "AI response to user:\nreply") {
		t.Fatalf("prompt missing response section: %q", prompt)
	}
	if strings.Contains(prompt, "<hidden>") || strings.Contains(prompt, "x</hidden>") || strings.Contains(prompt, "y</hidden>") {
		t.Fatalf("prompt still contains stripped XML content: %q", prompt)
	}
}

func TestRequestSummaryUserPromptOmitsEmptySections(t *testing.T) {
	prompt := RequestSummaryUserPrompt("", "think<internal>drop</internal>", "reply", "Japanese", "conversational")

	if strings.Contains(prompt, "User request:") {
		t.Fatalf("prompt unexpectedly contains empty user section: %q", prompt)
	}
	if !strings.Contains(prompt, "AI internal reasoning:\nthink") {
		t.Fatalf("prompt missing reasoning section: %q", prompt)
	}
	if !strings.Contains(prompt, "AI response to user:\nreply") {
		t.Fatalf("prompt missing response section: %q", prompt)
	}
	if strings.Contains(prompt, "drop") || strings.Contains(prompt, "<internal>") {
		t.Fatalf("prompt still contains stripped XML content: %q", prompt)
	}
}

func TestStripTranslationInputRemovesDatetimeAndXML(t *testing.T) {
	input := "Do the thing <reminder>hidden</reminder> at 2024-06-15T08:30:00Z and done.\nCurrent time: 2024-06-15T08:30:00Z\nMore text."
	got := StripTranslationInput(input)

	if strings.Contains(got, "hidden") || strings.Contains(got, "<reminder>") {
		t.Fatalf("StripTranslationInput should remove XML tags: %q", got)
	}
	if strings.Contains(got, "2024-06-15T08:30:00Z") {
		t.Fatalf("StripTranslationInput should remove ISO datetime: %q", got)
	}
	if strings.Contains(got, "Current time") {
		t.Fatalf("StripTranslationInput should remove current-time lines: %q", got)
	}
	if !strings.Contains(got, "Do the thing") || !strings.Contains(got, "and done") || !strings.Contains(got, "More text") {
		t.Fatalf("StripTranslationInput removed too much content: %q", got)
	}
}

func TestIsJapaneseLike(t *testing.T) {
	tests := []struct {
		lang string
		want bool
	}{
		{lang: "Japanese", want: true},
		{lang: "日本語", want: true},
		{lang: "Korean", want: true},
		{lang: "English", want: false},
	}

	for _, tt := range tests {
		got := IsJapaneseLike(tt.lang)
		if got != tt.want {
			t.Fatalf("IsJapaneseLike(%q) = %t, want %t", tt.lang, got, tt.want)
		}
	}
}

func TestStripTranslationOutput(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "strips current_datetime XML tag",
			text: "<current_datetime>2026-04-06T05:00:00Z</current_datetime>\n\nActual translation here.",
			want: "Actual translation here.",
		},
		{
			name: "strips English prompt header echo",
			text: "Language: Japanese\nFormat: Translate faithfully.\nTask: Produce summary.\n\nActual translated content.",
			want: "Actual translated content.",
		},
		{
			name: "strips Japanese prompt header echo",
			text: "言語: 日本語\n形式: 忠実に翻訳する。\nタスク: 要約を作成する。\n\n実際の翻訳内容。",
			want: "実際の翻訳内容。",
		},
		{
			name: "strips datetime + English header echo combined",
			text: "<current_datetime>2026-04-06T05:00:00Z</current_datetime>\n\nLanguage: Japanese\nFormat: Translate faithfully.\n\nActual translation here.",
			want: "Actual translation here.",
		},
		{
			name: "leaves clean output untouched",
			text: "Actual translation without any headers.",
			want: "Actual translation without any headers.",
		},
		{
			name: "leaves Japanese content starting with non-header",
			text: "これは翻訳結果です。\n続きの文章。",
			want: "これは翻訳結果です。\n続きの文章。",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripTranslationOutput(tt.text)
			if got != tt.want {
				t.Errorf("StripTranslationOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}
