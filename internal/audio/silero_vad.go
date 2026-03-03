//go:build onnx
// +build onnx

package audio

import (
	"fmt"
	"log"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// Compile-time check: SileroVAD satisfies core.VADProvider.
var _ core.VADProvider = (*SileroVAD)(nil)

// SileroVAD implements core.VADProvider using the Silero VAD v5 ONNX model.
//
// Silero VAD is a ~2MB neural network that classifies audio chunks as
// speech/non-speech with much higher accuracy than energy-based VAD.
// It maintains internal LSTM state (h, c) across calls for temporal context.
//
// Model inputs:
//
//	"input"  [1, chunk_size] float32  — audio samples (16kHz)
//	"sr"     [1] int64               — sample rate (16000)
//	"h"      [2, 1, 64] float32      — LSTM hidden state
//	"c"      [2, 1, 64] float32      — LSTM cell state
//
// Model outputs:
//
//	"output" [1, 1] float32           — speech probability [0, 1]
//	"hn"     [2, 1, 64] float32      — updated hidden state
//	"cn"     [2, 1, 64] float32      — updated cell state
type SileroVAD struct {
	mu      sync.Mutex
	session *ort.DynamicAdvancedSession

	// LSTM state carried across calls.
	hData []float32 // [2*1*64 = 128]
	cData []float32 // [2*1*64 = 128]

	// Schmitt trigger hysteresis for stable speech detection.
	triggered      bool
	enterThreshold float32 // probability to enter speech state (default 0.5)
	exitThreshold  float32 // probability to exit speech state (default 0.35)

	// Accumulation buffer for resampling to Silero's required chunk size.
	// Silero v5 with 16kHz expects 512-sample chunks.
	accumBuf []int16
}

const (
	sileroChunkSize = 512 // samples per Silero inference (32ms @ 16kHz)
	sileroStateSize = 128 // 2 * 1 * 64
)

// NewSileroVAD loads the Silero VAD ONNX model and returns a ready-to-use VAD.
//
// modelPath should point to the silero_vad.onnx file.
// Download from: https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx
func NewSileroVAD(modelPath string) (*SileroVAD, error) {
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("silero vad: failed to initialize ONNX runtime: %w", err)
	}

	inputNames := []string{"input", "sr", "h", "c"}
	outputNames := []string{"output", "hn", "cn"}

	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, nil)
	if err != nil {
		return nil, fmt.Errorf("silero vad: failed to create session: %w", err)
	}

	v := &SileroVAD{
		session:        session,
		hData:          make([]float32, sileroStateSize),
		cData:          make([]float32, sileroStateSize),
		enterThreshold: 0.5,
		exitThreshold:  0.35,
	}

	log.Printf("[silero-vad] model loaded from: %s", modelPath)
	return v, nil
}

// IsSpeech returns true if the given PCM chunk likely contains speech.
// Chunks are accumulated to sileroChunkSize (512 samples) before running
// inference.  Returns the latest classification for sub-chunk inputs.
//
// Thread-safe: may be called from the audio callback goroutine.
func (v *SileroVAD) IsSpeech(pcm []int16) bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.accumBuf = append(v.accumBuf, pcm...)

	// Process all complete chunks in the accumulation buffer.
	var lastResult bool
	processed := false
	for len(v.accumBuf) >= sileroChunkSize {
		chunk := v.accumBuf[:sileroChunkSize]
		v.accumBuf = v.accumBuf[sileroChunkSize:]

		prob := v.inferChunk(chunk)
		lastResult = v.applyHysteresis(prob)
		processed = true
	}

	if !processed {
		// Not enough data yet — return current triggered state.
		return v.triggered
	}
	return lastResult
}

// Reset clears LSTM state and accumulation buffer.
func (v *SileroVAD) Reset() {
	v.mu.Lock()
	defer v.mu.Unlock()

	for i := range v.hData {
		v.hData[i] = 0
	}
	for i := range v.cData {
		v.cData[i] = 0
	}
	v.accumBuf = v.accumBuf[:0]
	v.triggered = false
}

// Close releases ONNX resources.
func (v *SileroVAD) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.session != nil {
		v.session.Destroy()
		v.session = nil
	}
	return nil
}

// inferChunk runs a single Silero inference on a 512-sample chunk.
// Returns the speech probability [0, 1].
func (v *SileroVAD) inferChunk(pcm []int16) float32 {
	// Convert int16 → float32.
	audioData := make([]float32, sileroChunkSize)
	for i, s := range pcm {
		audioData[i] = float32(s) / 32768.0
	}

	// Create input tensors.
	inputTensor, err := ort.NewTensor(ort.NewShape(1, int64(sileroChunkSize)), audioData)
	if err != nil {
		log.Printf("[silero-vad] input tensor error: %v", err)
		return 0
	}
	defer inputTensor.Destroy()

	srData := []int64{int64(WhisperSampleRate)}
	srTensor, err := ort.NewTensor(ort.NewShape(1), srData)
	if err != nil {
		log.Printf("[silero-vad] sr tensor error: %v", err)
		return 0
	}
	defer srTensor.Destroy()

	hTensor, err := ort.NewTensor(ort.NewShape(2, 1, 64), v.hData)
	if err != nil {
		log.Printf("[silero-vad] h tensor error: %v", err)
		return 0
	}
	defer hTensor.Destroy()

	cTensor, err := ort.NewTensor(ort.NewShape(2, 1, 64), v.cData)
	if err != nil {
		log.Printf("[silero-vad] c tensor error: %v", err)
		return 0
	}
	defer cTensor.Destroy()

	// Run inference with auto-allocated outputs.
	outputs := []ort.Value{nil, nil, nil}
	err = v.session.Run(
		[]ort.Value{inputTensor, srTensor, hTensor, cTensor},
		outputs,
	)
	if err != nil {
		log.Printf("[silero-vad] inference error: %v", err)
		return 0
	}

	// Extract speech probability.
	var prob float32
	if outputTensor, ok := outputs[0].(*ort.Tensor[float32]); ok {
		data := outputTensor.GetData()
		if len(data) > 0 {
			prob = data[0]
		}
	}
	outputs[0].Destroy()

	// Update LSTM hidden state.
	if hnTensor, ok := outputs[1].(*ort.Tensor[float32]); ok {
		copy(v.hData, hnTensor.GetData())
	}
	outputs[1].Destroy()

	// Update LSTM cell state.
	if cnTensor, ok := outputs[2].(*ort.Tensor[float32]); ok {
		copy(v.cData, cnTensor.GetData())
	}
	outputs[2].Destroy()

	return prob
}

// applyHysteresis implements Schmitt trigger logic for stable speech detection.
// Uses two thresholds to prevent rapid toggling at speech boundaries.
func (v *SileroVAD) applyHysteresis(prob float32) bool {
	if !v.triggered && prob >= v.enterThreshold {
		v.triggered = true
	} else if v.triggered && prob < v.exitThreshold {
		v.triggered = false
	}
	return v.triggered
}
