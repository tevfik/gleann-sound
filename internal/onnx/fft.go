//go:build onnx
// +build onnx

package onnx

import "math"

// fftPaddedSize is the zero-padded FFT length.  Must be a power of 2 and >= nFFT (400).
const fftPaddedSize = 512

// fft computes the discrete Fourier transform of complex input (real, imag) in-place.
// Input length n must be a power of 2.  Uses Cooley-Tukey radix-2 decimation-in-time.
//
// This replaces the naive O(N²) DFT in computeMelSpectrogram, providing ~17× speedup
// for the mel-spectrogram computation (512 * log2(512) = 4608 vs 512 * 201 = 102912).
func fft(real, imag []float64, n int) {
	// Bit-reversal permutation.
	j := 0
	for i := 0; i < n-1; i++ {
		if i < j {
			real[i], real[j] = real[j], real[i]
			imag[i], imag[j] = imag[j], imag[i]
		}
		m := n >> 1
		for m >= 1 && j >= m {
			j -= m
			m >>= 1
		}
		j += m
	}

	// Butterfly stages.
	for stage := 1; stage < n; stage <<= 1 {
		angle := -math.Pi / float64(stage)
		wReal := math.Cos(angle)
		wImag := math.Sin(angle)
		for k := 0; k < n; k += stage << 1 {
			tReal, tImag := 1.0, 0.0
			for m := 0; m < stage; m++ {
				uIdx := k + m
				vIdx := k + m + stage
				vr := tReal*real[vIdx] - tImag*imag[vIdx]
				vi := tReal*imag[vIdx] + tImag*real[vIdx]
				real[vIdx] = real[uIdx] - vr
				imag[vIdx] = imag[uIdx] - vi
				real[uIdx] += vr
				imag[uIdx] += vi
				tReal, tImag = tReal*wReal-tImag*wImag, tReal*wImag+tImag*wReal
			}
		}
	}
}

// fftMagnitudeSquared computes |FFT(windowed_frame)|² for a single audio frame.
// The input frame is windowed, zero-padded to fftPaddedSize, and the first
// (nFFT/2 + 1) magnitude-squared bins are written to out.
//
// This function reuses the provided work buffers to avoid allocations in the hot loop.
func fftMagnitudeSquared(frame []float32, window []float32, fftReal, fftImag []float64, out []float32) {
	n := fftPaddedSize

	// Zero the work buffers.
	for i := range fftReal[:n] {
		fftReal[i] = 0
		fftImag[i] = 0
	}

	// Apply window and copy into real part (zero-padded beyond nFFT).
	frameLen := len(frame)
	if frameLen > nFFT {
		frameLen = nFFT
	}
	for i := 0; i < frameLen; i++ {
		fftReal[i] = float64(frame[i]) * float64(window[i])
	}

	// In-place FFT.
	fft(fftReal, fftImag, n)

	// Extract magnitude squared for first nFFT/2+1 bins.
	fftBins := nFFT/2 + 1
	for k := 0; k < fftBins; k++ {
		out[k] = float32(fftReal[k]*fftReal[k] + fftImag[k]*fftImag[k])
	}
}
