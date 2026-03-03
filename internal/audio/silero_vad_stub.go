//go:build !onnx
// +build !onnx

package audio

import "fmt"

// SileroVAD is a stub when building without the onnx tag.
type SileroVAD struct{}

// NewSileroVAD returns an error when ONNX Runtime is not available.
func NewSileroVAD(modelPath string) (*SileroVAD, error) {
	return nil, fmt.Errorf("silero vad: ONNX runtime not available (rebuild with -tags onnx)")
}
