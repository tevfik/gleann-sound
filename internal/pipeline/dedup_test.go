package pipeline

import (
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func TestDeduplicateOverlap(t *testing.T) {
	tests := []struct {
		name     string
		prev     string
		current  string
		expected string
	}{
		{
			name:     "basic overlap",
			prev:     "the quick brown fox jumps over",
			current:  "fox jumps over the lazy dog",
			expected: "the lazy dog",
		},
		{
			name:     "no overlap",
			prev:     "hello world",
			current:  "foo bar baz",
			expected: "foo bar baz",
		},
		{
			name:     "empty prev",
			prev:     "",
			current:  "hello world",
			expected: "hello world",
		},
		{
			name:     "empty current",
			prev:     "hello world",
			current:  "",
			expected: "",
		},
		{
			name:     "full overlap",
			prev:     "hello world",
			current:  "hello world",
			expected: "",
		},
		{
			name:     "single word overlap",
			prev:     "the quick brown fox",
			current:  "fox is sleeping",
			expected: "is sleeping",
		},
		{
			name:     "case insensitive overlap",
			prev:     "The Quick Brown FOX",
			current:  "fox is sleeping",
			expected: "is sleeping",
		},
		{
			name:     "long overlap",
			prev:     "bir iki üç dört beş altı yedi sekiz",
			current:  "beş altı yedi sekiz dokuz on",
			expected: "dokuz on",
		},
		{
			name:     "no prev no current",
			prev:     "",
			current:  "",
			expected: "",
		},
		{
			name:     "partial word no match",
			prev:     "testing",
			current:  "test results",
			expected: "test results",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeduplicateOverlap(tt.prev, tt.current)
			if got != tt.expected {
				t.Errorf("DeduplicateOverlap(%q, %q) = %q, want %q",
					tt.prev, tt.current, got, tt.expected)
			}
		})
	}
}

func TestCharLevelDedup(t *testing.T) {
	tests := []struct {
		name     string
		prev     string
		current  string
		expected string
	}{
		{
			name:     "syllable boundary turkish",
			prev:     "gündem kalenteri",
			current:  "kalenteri gün boyunca",
			expected: "gün boyunca",
		},
		{
			name:     "suffix prefix overlap",
			prev:     "bir toplantı yap",
			current:  "yapılacak işler",
			expected: "işler",
		},
		{
			name:     "no char overlap",
			prev:     "hello world",
			current:  "foo bar baz",
			expected: "foo bar baz",
		},
		{
			name:     "too short overlap ignored",
			prev:     "ab",
			current:  "abc def",
			expected: "abc def",
		},
		{
			name:     "exact suffix match full word",
			prev:     "merhabalar dünya",
			current:  "dünya güzel",
			expected: "güzel",
		},
		{
			name:     "case insensitive char dedup",
			prev:     "Hello World",
			current:  "world is great",
			expected: "is great",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := charLevelDedup(tt.prev, tt.current)
			if got != tt.expected {
				t.Errorf("charLevelDedup(%q, %q) = %q, want %q",
					tt.prev, tt.current, got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Timestamp-based dedup tests
// ---------------------------------------------------------------------------

func seg(startMs, endMs int, text string) core.Segment {
	return core.Segment{
		Start: time.Duration(startMs) * time.Millisecond,
		End:   time.Duration(endMs) * time.Millisecond,
		Text:  text,
	}
}

func TestDeduplicateByTimestamp_BasicOverlap(t *testing.T) {
	// 5s window, 3s step → 2s overlap.
	// Segments: [0-1s], [1-2.5s], [2.5-4s]
	// Midpoints: 0.5s, 1.75s, 3.25s
	// Only [2.5-4s] has midpoint >= 2s.
	segments := []core.Segment{
		seg(0, 1000, "hello"),
		seg(1000, 2500, "world is"),
		seg(2500, 4000, "great today"),
	}

	kept := DeduplicateByTimestamp(segments, 2.0)
	if len(kept) != 1 {
		t.Fatalf("expected 1 segment, got %d: %v", len(kept), kept)
	}
	if kept[0].Text != "great today" {
		t.Errorf("expected %q, got %q", "great today", kept[0].Text)
	}
}

func TestDeduplicateByTimestamp_NoOverlap(t *testing.T) {
	// overlapSec=0 → all segments kept.
	segments := []core.Segment{
		seg(0, 1000, "hello"),
		seg(1000, 2000, "world"),
	}

	kept := DeduplicateByTimestamp(segments, 0)
	if len(kept) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(kept))
	}
}

func TestDeduplicateByTimestamp_AllInOverlap(t *testing.T) {
	// All segments within 3s overlap → nothing kept.
	segments := []core.Segment{
		seg(0, 1000, "hello"),
		seg(1000, 2000, "world"),
		seg(2000, 2500, "foo"),
	}

	kept := DeduplicateByTimestamp(segments, 3.0)
	if len(kept) != 0 {
		t.Fatalf("expected 0 segments, got %d: %v", len(kept), kept)
	}
}

func TestDeduplicateByTimestamp_AllBeyondOverlap(t *testing.T) {
	// All segments beyond 0.5s overlap → all kept.
	segments := []core.Segment{
		seg(1000, 2000, "hello"),
		seg(2000, 3000, "world"),
	}

	kept := DeduplicateByTimestamp(segments, 0.5)
	if len(kept) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(kept))
	}
}

func TestDeduplicateByTimestamp_EmptySegments(t *testing.T) {
	kept := DeduplicateByTimestamp(nil, 2.0)
	if len(kept) != 0 {
		t.Fatalf("expected 0 segments for nil input, got %d", len(kept))
	}

	kept = DeduplicateByTimestamp([]core.Segment{}, 2.0)
	if len(kept) != 0 {
		t.Fatalf("expected 0 segments for empty input, got %d", len(kept))
	}
}

func TestDeduplicateByTimestamp_MidpointEdgeCase(t *testing.T) {
	// Segment midpoint exactly at overlap boundary (2.0s) → should be kept (>=).
	// Segment [1s - 3s] → midpoint = 2.0s, overlap = 2.0s → kept.
	segments := []core.Segment{
		seg(1000, 3000, "edge case"),
	}

	kept := DeduplicateByTimestamp(segments, 2.0)
	if len(kept) != 1 {
		t.Fatalf("expected 1 segment (midpoint at boundary), got %d", len(kept))
	}
	if kept[0].Text != "edge case" {
		t.Errorf("expected %q, got %q", "edge case", kept[0].Text)
	}
}

func TestJoinSegments(t *testing.T) {
	tests := []struct {
		name     string
		segments []core.Segment
		expected string
	}{
		{
			name:     "multiple segments",
			segments: []core.Segment{seg(0, 1000, "hello"), seg(1000, 2000, "world")},
			expected: "hello world",
		},
		{
			name:     "empty text skipped",
			segments: []core.Segment{seg(0, 1000, "hello"), seg(1000, 2000, ""), seg(2000, 3000, "world")},
			expected: "hello world",
		},
		{
			name:     "whitespace only skipped",
			segments: []core.Segment{seg(0, 1000, "  "), seg(1000, 2000, "hello")},
			expected: "hello",
		},
		{
			name:     "single segment",
			segments: []core.Segment{seg(0, 1000, "hello")},
			expected: "hello",
		},
		{
			name:     "no segments",
			segments: nil,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JoinSegments(tt.segments)
			if got != tt.expected {
				t.Errorf("JoinSegments() = %q, want %q", got, tt.expected)
			}
		})
	}
}
