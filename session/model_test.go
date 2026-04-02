package session

import (
	"encoding/json"
	"testing"
)

func TestParseAssistantMessage(t *testing.T) {
	evt := SessionEvent{
		Data: json.RawMessage(`{"messageId":"m1","content":"answer","interactionId":"i1","reasoningText":"think","outputTokens":7}`),
	}

	msg, err := ParseAssistantMessage(evt)
	if err != nil {
		t.Fatalf("ParseAssistantMessage() error = %v", err)
	}
	if msg.MessageID != "m1" || msg.Content != "answer" || msg.InteractionID != "i1" || msg.ReasoningText != "think" || msg.OutputTokens != 7 {
		t.Fatalf("ParseAssistantMessage() = %+v, want decoded assistant payload", msg)
	}
}

func TestParseUserMessage(t *testing.T) {
	evt := SessionEvent{
		Data: json.RawMessage(`{"content":"hello","source":"cli"}`),
	}

	msg, err := ParseUserMessage(evt)
	if err != nil {
		t.Fatalf("ParseUserMessage() error = %v", err)
	}
	if msg.Content != "hello" || msg.Source != "cli" {
		t.Fatalf("ParseUserMessage() = %+v, want decoded user payload", msg)
	}
}

func TestParseMessageRejectsInvalidJSON(t *testing.T) {
	evt := SessionEvent{Data: json.RawMessage(`{"content":`)}

	if _, err := ParseAssistantMessage(evt); err == nil {
		t.Fatal("ParseAssistantMessage() error = nil, want decode error")
	}
	if _, err := ParseUserMessage(evt); err == nil {
		t.Fatal("ParseUserMessage() error = nil, want decode error")
	}
}
