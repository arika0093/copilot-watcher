package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/copilot-watcher/copilot-watcher/session"
)

func TestVisibleLinesFromBottomPadsAndAnchorsLatest(t *testing.T) {
	visible, offset, start, end := visibleLinesFromBottom([]string{"line1", "line2", "line3"}, 5, 0)

	if offset != 0 || start != 0 || end != 3 {
		t.Fatalf("viewport metadata = (%d, %d, %d), want (0, 0, 3)", offset, start, end)
	}
	want := []string{"", "", "line1", "line2", "line3"}
	if len(visible) != len(want) {
		t.Fatalf("len(visible) = %d, want %d", len(visible), len(want))
	}
	for i := range want {
		if visible[i] != want[i] {
			t.Fatalf("visible[%d] = %q, want %q", i, visible[i], want[i])
		}
	}
}

func TestVisibleLinesFromBottomScrollsTowardOlderContent(t *testing.T) {
	visible, offset, start, end := visibleLinesFromBottom([]string{"1", "2", "3", "4", "5"}, 3, 1)

	if offset != 1 || start != 1 || end != 4 {
		t.Fatalf("viewport metadata = (%d, %d, %d), want (1, 1, 4)", offset, start, end)
	}
	want := []string{"2", "3", "4"}
	for i := range want {
		if visible[i] != want[i] {
			t.Fatalf("visible[%d] = %q, want %q", i, visible[i], want[i])
		}
	}
}

func TestVisibleLinesFromBottomClampsOverscroll(t *testing.T) {
	visible, offset, start, end := visibleLinesFromBottom([]string{"1", "2", "3", "4"}, 2, 999)

	if offset != 2 || start != 0 || end != 2 {
		t.Fatalf("viewport metadata = (%d, %d, %d), want (2, 0, 2)", offset, start, end)
	}
	want := []string{"1", "2"}
	for i := range want {
		if visible[i] != want[i] {
			t.Fatalf("visible[%d] = %q, want %q", i, visible[i], want[i])
		}
	}
}

func TestClampBottomOffsetForRangeShowsOlderSelection(t *testing.T) {
	got := clampBottomOffsetForRange(20, 5, 0, 4, 8)
	if got != 11 {
		t.Fatalf("clampBottomOffsetForRange() = %d, want %d", got, 11)
	}
}

func TestBuildRTLinesStacksAllTurns(t *testing.T) {
	m := ViewerModel{
		rtTurns: []*viewTurn{
			{turnNum: 1, userMsg: "first request", reasoning: "first reasoning", isReasoning: true, done: true, timestamp: time.Now().Add(-2 * time.Minute)},
			{turnNum: 2, userMsg: "second request", response: "second response", done: true, timestamp: time.Now().Add(-1 * time.Minute)},
		},
	}

	joined := stripANSI(strings.Join(m.buildRTLines(80), "\n"))
	for _, want := range []string{"Request 1 / 2", "Request 2 / 2", "first request", "second request", "second response"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("buildRTLines() missing %q in output: %q", want, joined)
		}
	}
}

func TestBuildTurnBlockOmitsResponseWhenDisabled(t *testing.T) {
	turn := &viewTurn{
		userMsg:     "request",
		reasoning:   "reasoning",
		response:    "response text",
		isReasoning: true,
		done:        true,
	}

	joined := stripANSI(strings.Join(buildTurnBlock(turn, 80, 0, false), "\n"))
	if strings.Contains(joined, "Response") || strings.Contains(joined, "response text") {
		t.Fatalf("buildTurnBlock() unexpectedly rendered response: %q", joined)
	}
}

func TestBuildTurnBlockShowsPlaceholderWithoutResponseInRequests(t *testing.T) {
	turn := &viewTurn{
		userMsg:  "request",
		response: "response text",
		done:     true,
	}

	joined := stripANSI(strings.Join(buildTurnBlock(turn, 80, 0, false), "\n"))
	if strings.Contains(joined, "response text") {
		t.Fatalf("buildTurnBlock() unexpectedly rendered raw response: %q", joined)
	}
	if !strings.Contains(joined, "No reasoning summary available") {
		t.Fatalf("buildTurnBlock() missing placeholder: %q", joined)
	}
}

func TestBuildHeaderLinesWrapAndViewFitsHeight(t *testing.T) {
	m := ViewerModel{
		info: session.SessionInfo{
			SessionID:   "1234567890abcdef",
			Cwd:         strings.Repeat("very-long-path/", 6),
			SourceLabel: "VS Code",
		},
		status:       "Monitoring a very long status message for header wrapping",
		statusOK:     true,
		outputLang:   "Japanese",
		outputFormat: "conversational",
		ready:        true,
		width:        50,
		height:       12,
		scroll:       map[TabID]int{},
	}

	headerLines := m.buildHeaderLines(m.width)
	if len(headerLines) < 2 {
		t.Fatalf("buildHeaderLines() = %d lines, want at least 2", len(headerLines))
	}

	viewLines := strings.Split(stripANSI(m.View()), "\n")
	if len(viewLines) != m.height {
		t.Fatalf("len(View lines) = %d, want %d", len(viewLines), m.height)
	}
	if !strings.Contains(viewLines[0], "copilot-watcher") {
		t.Fatalf("first header line missing expected content: %q", viewLines[0])
	}
}

func stripANSI(s string) string {
	ansi := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansi.ReplaceAllString(s, "")
}
