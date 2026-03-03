// Package pipeline implements the streaming transcription pipeline with
// sliding-window inference, text deduplication, and context carryover.
package pipeline

import (
	"strings"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// DeduplicateOverlap removes the overlapping prefix from currentText that
// matches a suffix of prevText.  This handles the text duplication that
// occurs when consecutive sliding windows overlap in time.
//
// First attempts word-level matching (exact word boundaries), then falls back
// to character-level suffix/prefix matching for cases where Whisper splits
// words across window boundaries.
//
// Example:
//
//	prev:    "the quick brown fox jumps over"
//	current: "fox jumps over the lazy dog"
//	result:  "the lazy dog"
func DeduplicateOverlap(prevText, currentText string) string {
	prevWords := strings.Fields(prevText)
	currWords := strings.Fields(currentText)

	if len(prevWords) == 0 || len(currWords) == 0 {
		return currentText
	}

	// Find the longest suffix of prev that matches a prefix of current.
	maxCheck := len(prevWords)
	if len(currWords) < maxCheck {
		maxCheck = len(currWords)
	}

	bestMatch := 0
	for suffLen := 1; suffLen <= maxCheck; suffLen++ {
		match := true
		for i := 0; i < suffLen; i++ {
			if !strings.EqualFold(prevWords[len(prevWords)-suffLen+i], currWords[i]) {
				match = false
				break
			}
		}
		if match {
			bestMatch = suffLen
		}
	}

	if bestMatch > 0 {
		remaining := currWords[bestMatch:]
		if len(remaining) == 0 {
			return ""
		}
		return strings.Join(remaining, " ")
	}

	// Word-level matching found nothing — try character-level fallback.
	// This catches cases where Whisper breaks words at syllable boundaries
	// (e.g. prev="kalenteri" / curr="nteri gün" → result="gün").
	return charLevelDedup(prevText, currentText)
}

// charLevelDedup finds the longest suffix of prev that matches a prefix of
// curr at the character level, then returns curr with the overlap removed.
// Uses rune-aware matching and snaps to a word boundary after the overlap.
func charLevelDedup(prev, curr string) string {
	prevRunes := []rune(strings.TrimSpace(prev))
	currRunes := []rune(strings.TrimSpace(curr))

	maxCheck := 40 // max chars to check
	if len(prevRunes) < maxCheck {
		maxCheck = len(prevRunes)
	}
	if len(currRunes) < maxCheck {
		maxCheck = len(currRunes)
	}

	bestOverlap := 0
	for n := 3; n <= maxCheck; n++ { // minimum 3 chars to avoid false positives
		suffix := strings.ToLower(string(prevRunes[len(prevRunes)-n:]))
		prefix := strings.ToLower(string(currRunes[:n]))
		if suffix == prefix {
			bestOverlap = n
		}
	}

	if bestOverlap >= 3 {
		result := strings.TrimSpace(string(currRunes[bestOverlap:]))
		// If the overlap ended mid-word (next char is not a space and
		// the character after overlap is not the start of a new word),
		// trim the partial word remnant to snap to the next word boundary.
		if len(result) > 0 && bestOverlap < len(currRunes) &&
			currRunes[bestOverlap] != ' ' && currRunes[bestOverlap-1] != ' ' {
			// Check if the overlap aligns with the end of a word in curr.
			// If currRunes[bestOverlap] is mid-word, skip to next space.
			if idx := strings.IndexByte(result, ' '); idx >= 0 {
				result = strings.TrimSpace(result[idx:])
			}
		}
		return result
	}

	return curr
}

// ---------------------------------------------------------------------------
// Timestamp-based deduplication
// ---------------------------------------------------------------------------

// DeduplicateByTimestamp keeps only segments whose temporal midpoint falls
// at or beyond the overlap region.  This is more reliable than text matching
// because it uses whisper's own segment timestamps to decide what is "new"
// content vs. repeated overlap content.
//
// overlapSec is the overlap duration in seconds (WindowSizeSec - StepSizeSec).
// For the default config (5s window, 3s step) this is 2.0s.
//
// Heuristic: a segment's midpoint = (Start + End) / 2.  If midpoint >= overlap
// duration, the segment's centre of mass is in the "new" part of the window,
// so we keep it.  Otherwise it belongs to the overlap (already emitted by the
// previous window) and we discard it.
func DeduplicateByTimestamp(segments []core.Segment, overlapSec float64) []core.Segment {
	if len(segments) == 0 || overlapSec <= 0 {
		return segments
	}

	overlapDur := time.Duration(overlapSec * float64(time.Second))

	var kept []core.Segment
	for _, seg := range segments {
		midpoint := seg.Start + (seg.End-seg.Start)/2
		if midpoint >= overlapDur {
			kept = append(kept, seg)
		}
	}
	return kept
}

// JoinSegments concatenates the text of the given segments with a single
// space between each.  Empty-text segments are skipped.
func JoinSegments(segments []core.Segment) string {
	var sb strings.Builder
	for _, seg := range segments {
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(text)
	}
	return sb.String()
}
