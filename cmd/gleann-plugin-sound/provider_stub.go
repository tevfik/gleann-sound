//go:build !onnx
// +build !onnx

package main

// setONNXProvider is a no-op when building without the onnx tag.
func setONNXProvider(_ string) {}
