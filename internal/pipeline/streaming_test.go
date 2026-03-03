package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// mockTranscriber is a test double for core.StreamingTranscriber.
type mockTranscriber struct {
	mu      sync.Mutex
	calls   int
	results []string // pre-canned results to return in sequence
}

func (m *mockTranscriber) TranscribeStream(_ context.Context, pcm []int16) (string, error) {
	return "", nil
}

func (m *mockTranscriber) TranscribeStreamSegments(_ context.Context, pcm []int16) ([]core.Segment, error) {
	return nil, nil
}

func (m *mockTranscriber) TranscribeFile(_ context.Context, _ string) ([]core.Segment, error) {
	return nil, nil
}

func (m *mockTranscriber) SetLanguage(_ string) {}

func (m *mockTranscriber) Close() error { return nil }

func (m *mockTranscriber) ResetStream() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = 0
}

func (m *mockTranscriber) TranscribeWindow(_ context.Context, pcm []int16, promptText string) (core.StreamResult, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.calls
	m.calls++

	var text string
	if idx < len(m.results) {
		text = m.results[idx]
	} else {
		dur := float64(len(pcm)) / SampleRate
		text = fmt.Sprintf("window-%d (%.1fs)", idx, dur)
	}

	result := core.StreamResult{
		Text:      text,
		WindowSeq: idx,
	}
	return result, text, nil
}

// mockVAD always returns true (speech detected).
type mockVAD struct{}

func (v *mockVAD) IsSpeech(_ []int16) bool { return true }
func (v *mockVAD) Reset()                  {}

// silentVAD always returns false (no speech).
type silentVAD struct{}

func (v *silentVAD) IsSpeech(_ []int16) bool { return false }
func (v *silentVAD) Reset()                  {}

func TestStreamingPipeline_BasicWindowing(t *testing.T) {
	transcriber := &mockTranscriber{
		results: []string{"hello world", "world is great", "great day today"},
	}

	pipe := NewStreamingPipeline(transcriber, &mockVAD{}, Config{
		WindowSizeSec: 1.0,  // 16000 samples
		StepSizeSec:   0.5,  // 8000 samples
		MinSpeechSec:  0.01, // very low threshold for testing
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audioCh := make(chan []int16, 64)

	var results []string
	var resultsMu sync.Mutex
	done := make(chan struct{})

	go func() {
		_ = pipe.Run(ctx, audioCh, func(result core.StreamResult) {
			resultsMu.Lock()
			results = append(results, result.Text)
			resultsMu.Unlock()
		})
		close(done)
	}()

	// Send enough audio for 3 windows.
	// Step = 8000 samples, so 3 steps = 24000 samples.
	// Send in 480-sample chunks (30ms each).
	totalSamples := 24000
	chunkSize := 480
	for sent := 0; sent < totalSamples; sent += chunkSize {
		chunk := make([]int16, chunkSize)
		// Fill with non-zero data so VAD passes.
		for i := range chunk {
			chunk[i] = 1000
		}
		audioCh <- chunk
	}

	// Close channel to signal end.
	close(audioCh)
	<-done

	resultsMu.Lock()
	defer resultsMu.Unlock()

	if len(results) == 0 {
		t.Fatal("expected at least one transcription result, got 0")
	}

	t.Logf("got %d results: %v", len(results), results)
}

func TestStreamingPipeline_SilenceSkipped(t *testing.T) {
	transcriber := &mockTranscriber{
		results: []string{"should not see this"},
	}

	pipe := NewStreamingPipeline(transcriber, &silentVAD{}, Config{
		WindowSizeSec: 1.0,
		StepSizeSec:   0.5,
		MinSpeechSec:  0.3,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	audioCh := make(chan []int16, 64)
	var results []string
	done := make(chan struct{})

	go func() {
		_ = pipe.Run(ctx, audioCh, func(result core.StreamResult) {
			results = append(results, result.Text)
		})
		close(done)
	}()

	// Send 2 seconds of silence.
	for i := 0; i < 32000/480; i++ {
		audioCh <- make([]int16, 480)
	}
	close(audioCh)
	<-done

	if len(results) != 0 {
		t.Errorf("expected 0 results (all silence), got %d: %v", len(results), results)
	}
}

func TestStreamingPipeline_Deduplication(t *testing.T) {
	// Simulate overlapping text output.
	transcriber := &mockTranscriber{
		results: []string{
			"the quick brown fox",
			"brown fox jumps over",
			"jumps over the lazy dog",
		},
	}

	pipe := NewStreamingPipeline(transcriber, &mockVAD{}, Config{
		WindowSizeSec: 1.0,
		StepSizeSec:   0.5,
		MinSpeechSec:  0.01,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audioCh := make(chan []int16, 64)
	var results []string
	var resultsMu sync.Mutex
	done := make(chan struct{})

	go func() {
		_ = pipe.Run(ctx, audioCh, func(result core.StreamResult) {
			resultsMu.Lock()
			results = append(results, result.Text)
			resultsMu.Unlock()
		})
		close(done)
	}()

	// Send enough for 3 steps.
	totalSamples := 24000
	chunkSize := 480
	for sent := 0; sent < totalSamples; sent += chunkSize {
		chunk := make([]int16, chunkSize)
		for i := range chunk {
			chunk[i] = 1000
		}
		audioCh <- chunk
	}
	close(audioCh)
	<-done

	resultsMu.Lock()
	defer resultsMu.Unlock()

	// First result should be full, subsequent ones should be deduplicated.
	combined := strings.Join(results, " ")
	t.Logf("combined output: %q", combined)

	// Should not have duplicate "brown fox" or "jumps over" in the combined output.
	if strings.Count(combined, "brown fox") > 1 {
		t.Errorf("deduplication failed: 'brown fox' appears %d times in %q",
			strings.Count(combined, "brown fox"), combined)
	}
}

// ---------------------------------------------------------------------------
// Timestamp-based dedup integration test
// ---------------------------------------------------------------------------

// segmentedResult holds a pre-canned TranscribeWindow response with segments.
type segmentedResult struct {
	text     string
	segments []core.Segment
}

// segmentedMockTranscriber returns results with per-segment timestamps,
// enabling timestamp-based dedup in the pipeline.
type segmentedMockTranscriber struct {
	mu      sync.Mutex
	calls   int
	results []segmentedResult
}

func (m *segmentedMockTranscriber) TranscribeStream(_ context.Context, _ []int16) (string, error) {
	return "", nil
}
func (m *segmentedMockTranscriber) TranscribeStreamSegments(_ context.Context, _ []int16) ([]core.Segment, error) {
	return nil, nil
}
func (m *segmentedMockTranscriber) TranscribeFile(_ context.Context, _ string) ([]core.Segment, error) {
	return nil, nil
}
func (m *segmentedMockTranscriber) SetLanguage(_ string) {}
func (m *segmentedMockTranscriber) Close() error         { return nil }
func (m *segmentedMockTranscriber) ResetStream() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = 0
}

func (m *segmentedMockTranscriber) TranscribeWindow(_ context.Context, pcm []int16, promptText string) (core.StreamResult, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.calls
	m.calls++

	if idx < len(m.results) {
		r := m.results[idx]
		return core.StreamResult{
			Text:      r.text,
			Segments:  r.segments,
			WindowSeq: idx,
		}, r.text, nil
	}

	dur := float64(len(pcm)) / SampleRate
	text := fmt.Sprintf("window-%d (%.1fs)", idx, dur)
	return core.StreamResult{
		Text:      text,
		WindowSeq: idx,
	}, text, nil
}

func TestStreamingPipeline_TimestampDedup(t *testing.T) {
	// Config: 1s window, 0.5s step → 0.5s overlap.
	// Window 0 (seq=0): full window, no dedup applied (first window).
	//   Segments: "hello" [0-0.3s], "world" [0.3-0.8s]
	// Window 1 (seq=1): overlap region is 0-0.5s.
	//   Segments: "world" [0-0.4s] (midpoint=0.2s < 0.5s → filtered)
	//             "is great" [0.5-0.9s] (midpoint=0.7s >= 0.5s → kept)
	// Window 2 (seq=2): overlap region is 0-0.5s.
	//   Segments: "great" [0.1-0.4s] (midpoint=0.25s < 0.5s → filtered)
	//             "today" [0.5-0.8s] (midpoint=0.65s >= 0.5s → kept)
	transcriber := &segmentedMockTranscriber{
		results: []segmentedResult{
			{
				text: "hello world",
				segments: []core.Segment{
					{Start: 0, End: 300 * time.Millisecond, Text: "hello"},
					{Start: 300 * time.Millisecond, End: 800 * time.Millisecond, Text: "world"},
				},
			},
			{
				text: "world is great",
				segments: []core.Segment{
					{Start: 0, End: 400 * time.Millisecond, Text: "world"},
					{Start: 500 * time.Millisecond, End: 900 * time.Millisecond, Text: "is great"},
				},
			},
			{
				text: "great today",
				segments: []core.Segment{
					{Start: 100 * time.Millisecond, End: 400 * time.Millisecond, Text: "great"},
					{Start: 500 * time.Millisecond, End: 800 * time.Millisecond, Text: "today"},
				},
			},
		},
	}

	pipe := NewStreamingPipeline(transcriber, &mockVAD{}, Config{
		WindowSizeSec: 1.0,
		StepSizeSec:   0.5,
		MinSpeechSec:  0.01,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audioCh := make(chan []int16, 64)
	var results []string
	var resultsMu sync.Mutex
	done := make(chan struct{})

	go func() {
		_ = pipe.Run(ctx, audioCh, func(result core.StreamResult) {
			resultsMu.Lock()
			results = append(results, result.Text)
			resultsMu.Unlock()
		})
		close(done)
	}()

	// Send enough for 3 steps (3 × 8000 = 24000 samples).
	totalSamples := 24000
	chunkSize := 480
	for sent := 0; sent < totalSamples; sent += chunkSize {
		chunk := make([]int16, chunkSize)
		for i := range chunk {
			chunk[i] = 1000
		}
		audioCh <- chunk
	}
	close(audioCh)
	<-done

	resultsMu.Lock()
	defer resultsMu.Unlock()

	combined := strings.Join(results, " ")
	t.Logf("timestamp dedup results: %v", results)
	t.Logf("combined: %q", combined)

	// "world" should appear only once (from window 0); window 1's "world" should be filtered.
	if strings.Count(combined, "world") > 1 {
		t.Errorf("timestamp dedup failed: 'world' appears %d times in %q",
			strings.Count(combined, "world"), combined)
	}

	// "great" should appear only once (from window 1); window 2's "great" should be filtered.
	if strings.Count(combined, "great") > 1 {
		t.Errorf("timestamp dedup failed: 'great' appears %d times in %q",
			strings.Count(combined, "great"), combined)
	}

	// The combined output should contain all unique content.
	for _, word := range []string{"hello", "world", "is great", "today"} {
		if !strings.Contains(combined, word) {
			t.Errorf("expected %q in combined output %q", word, combined)
		}
	}
}
