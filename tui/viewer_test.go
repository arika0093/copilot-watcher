package tui

import "testing"

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
