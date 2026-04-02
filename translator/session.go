package translator

import (
	"context"
	"fmt"

	copilot "github.com/github/copilot-sdk/go"
)

// Session IDs for copilot-watcher's own internal sessions.
// These prefixed IDs are excluded from the session list displayed in the UI.
const (
	RTSessionID   = "copilot-watcher-rt"
	HistSessionID = "copilot-watcher-hist"
)

// Translator manages two Copilot SDK sessions:
//   - rtSession: persistent real-time session; context accumulates across turns
//   - histSession: isolated session for history/session summaries
type Translator struct {
	client       *copilot.Client
	rtSession    *copilot.Session
	histSession  *copilot.Session
	outputLang   string
	outputFormat string // format code: "bullets", "numbered", "prose", or custom text
	LogFunc      func(string)
}

// SetLogger sets the logging callback used to emit diagnostic messages.
func (t *Translator) SetLogger(fn func(string)) { t.LogFunc = fn }

// log emits a diagnostic message via LogFunc (if set).
func (t *Translator) log(msg string) {
	if t.LogFunc != nil {
		t.LogFunc(msg)
	}
}

// New creates a Translator with two dedicated Copilot sessions.
// logCh receives diagnostic log messages; pass nil to disable logging.
func New(logCh chan string) (*Translator, error) {
	t := &Translator{
		outputLang:   "Japanese",
		outputFormat: "conversational",
	}
	if logCh != nil {
		t.LogFunc = func(msg string) {
			select {
			case logCh <- msg:
			default:
			}
		}
	}

	t.log("Copilot SDK: client starting...")
	client := copilot.NewClient(nil)
	if err := client.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("starting Copilot client: %w", err)
	}
	t.log("Copilot SDK: client started OK")

	const model = "gpt-5-mini"
	const effort = "low"

	rtSession, err := client.CreateSession(context.Background(), &copilot.SessionConfig{
		SessionID:           RTSessionID,
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		Model:               model,
		ReasoningEffort:     effort,
		Streaming:           true,
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "replace",
			Content: RTSystemPrompt(),
		},
	})
	if err != nil {
		client.Stop() //nolint:errcheck
		return nil, fmt.Errorf("creating RT session: %w", err)
	}
	t.log("Copilot SDK: session created (rtSession)")

	histSession, err := client.CreateSession(context.Background(), &copilot.SessionConfig{
		SessionID:           HistSessionID,
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
		Model:               model,
		ReasoningEffort:     effort,
		Streaming:           true,
		SystemMessage: &copilot.SystemMessageConfig{
			Mode:    "replace",
			Content: HistSystemPrompt(),
		},
	})
	if err != nil {
		rtSession.Disconnect() //nolint:errcheck
		client.Stop()          //nolint:errcheck
		return nil, fmt.Errorf("creating history session: %w", err)
	}
	t.log("Copilot SDK: session created (histSession)")

	t.client = client
	t.rtSession = rtSession
	t.histSession = histSession
	return t, nil
}

// SetLanguage updates the output language for future translations.
func (t *Translator) SetLanguage(lang string) { t.outputLang = lang }

// GetLanguage returns the current output language.
func (t *Translator) GetLanguage() string { return t.outputLang }

// SetFormat updates the output format for future translations.
func (t *Translator) SetFormat(format string) { t.outputFormat = format }

// GetFormat returns the current output format code.
func (t *Translator) GetFormat() string { return t.outputFormat }

// Close cleans up all sessions and the client.
func (t *Translator) Close() {
	t.log("Copilot SDK: shutting down sessions")
	if t.rtSession != nil {
		t.rtSession.Disconnect() //nolint:errcheck
	}
	if t.histSession != nil {
		t.histSession.Disconnect() //nolint:errcheck
	}
	if t.client != nil {
		t.client.Stop() //nolint:errcheck
	}
}

// StreamErrPrefix is a sentinel prefix written to the stream channel when a
// session-level error occurs. Consumers should check for this prefix and handle
// it as an error rather than appending it to the translation output.
const StreamErrPrefix = "\x01STREAM_ERROR:"

// stream sends a user prompt to a session and streams response chunks.
func (t *Translator) stream(ctx context.Context, sess *copilot.Session, sessionID, userPrompt string) (<-chan string, error) {
	t.log(fmt.Sprintf("Copilot SDK: stream started for session %s", sessionID))
	ch := make(chan string, 32)
	done := make(chan struct{})
	var gotDelta bool

	unsubscribe := sess.On(func(evt copilot.SessionEvent) {
		switch evt.Type {
		case copilot.SessionEventTypeAssistantMessageDelta:
			if evt.Data.DeltaContent != nil && *evt.Data.DeltaContent != "" {
				gotDelta = true
				select {
				case ch <- *evt.Data.DeltaContent:
				default:
				}
			}
		case copilot.SessionEventTypeAssistantMessage:
			if !gotDelta && evt.Data.Content != nil && *evt.Data.Content != "" {
				ch <- *evt.Data.Content
			}
		case copilot.SessionEventTypeSessionIdle:
			select {
			case <-done:
			default:
				close(done)
			}
		case copilot.SessionEventTypeSessionError:
			if evt.Data.Message != nil {
				errMsg := *evt.Data.Message
				t.log(fmt.Sprintf("Copilot SDK: session error in %s: %s", sessionID, errMsg))
				// Send tagged error sentinel instead of mixing with translation text.
				select {
				case ch <- StreamErrPrefix + errMsg:
				default:
				}
			}
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})

	go func() {
		defer close(ch)
		defer unsubscribe()
		select {
		case <-done:
		case <-ctx.Done():
			t.log(fmt.Sprintf("Copilot SDK: stream cancelled for session %s", sessionID))
		}
	}()

	if _, err := sess.Send(ctx, copilot.MessageOptions{Prompt: userPrompt}); err != nil {
		close(done)
		return nil, fmt.Errorf("sending: %w", err)
	}
	return ch, nil
}

// Translate streams a real-time turn summary using the persistent RT session.
// The RT session accumulates context across turns for coherent ongoing translation.
func (t *Translator) Translate(ctx context.Context, reasoningText string) (<-chan string, error) {
	return t.stream(ctx, t.rtSession, RTSessionID, TranslateUserPrompt(reasoningText, t.outputLang, t.outputFormat))
}

// TranslateTurn streams a single-turn translation using the isolated history session.
// Used by the History/Turns tab so each turn is translated independently.
func (t *Translator) TranslateTurn(ctx context.Context, reasoningText string) (<-chan string, error) {
	return t.stream(ctx, t.histSession, HistSessionID, TranslateUserPrompt(reasoningText, t.outputLang, t.outputFormat))
}
func (t *Translator) SummarizeSession(ctx context.Context, label, reasoningText string) (<-chan string, error) {
	return t.stream(ctx, t.histSession, HistSessionID, SessionSummaryUserPrompt(label, reasoningText, t.outputLang, t.outputFormat))
}

// SummarizeAll streams an all-sessions unified summary using the isolated history session.
func (t *Translator) SummarizeAll(ctx context.Context, reasoningText string) (<-chan string, error) {
	return t.stream(ctx, t.histSession, HistSessionID, AllSessionsUserPrompt(reasoningText, t.outputLang, t.outputFormat))
}
