// Package httpserver implements the HTTP plugin server for gleann integration.
//
// It exposes two endpoints that conform to the gleann PluginManager contract:
//
//	GET  /health  → {"status":"ok","plugin":"gleann-plugin-sound","capabilities":["document-extraction"]}
//	POST /convert → accepts multipart file, transcribes via Whisper, returns {"markdown":"..."}
//
// This allows gleann's readDocuments pipeline to automatically transcribe
// audio/video files during index building, just like gleann-docs handles PDFs.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// EngineFactory creates a Transcriber on demand. Used for lazy model loading.
type EngineFactory func() (core.Transcriber, error)

// Server is a lightweight HTTP plugin server for gleann document extraction.
type Server struct {
	factory  EngineFactory
	language string
	port     int

	// Lazy-loaded engine with mutex for thread safety.
	mu     sync.Mutex
	engine core.Transcriber
}

// New creates a new plugin HTTP server.
// The engine is loaded lazily on the first /convert request so that the
// health endpoint responds immediately (within the PluginManager's 10s timeout).
func New(factory EngineFactory, language string, port int) *Server {
	return &Server{
		factory:  factory,
		language: language,
		port:     port,
	}
}

// Serve starts the HTTP server. Blocks until the server is shut down.
func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/convert", s.handleConvert)

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	log.Printf("[plugin-serve] gleann-plugin-sound HTTP plugin on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// Close releases the transcription engine if it was loaded.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		return s.engine.Close()
	}
	return nil
}

// getEngine returns the transcription engine, initialising it on first call.
func (s *Server) getEngine() (core.Transcriber, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		return s.engine, nil
	}
	log.Println("[plugin-serve] loading transcription model (first request)...")
	engine, err := s.factory()
	if err != nil {
		return nil, err
	}
	if s.language != "" {
		engine.SetLanguage(s.language)
	}
	s.engine = engine
	log.Println("[plugin-serve] model loaded successfully")
	return s.engine, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"plugin":       "gleann-plugin-sound",
		"capabilities": []string{"document-extraction"},
	})
}

func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	// Parse multipart form — 256 MB max (enough for most audio/video files).
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("parse multipart: %v", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing 'file' field: %v", err))
		return
	}
	defer file.Close()

	// Save to temp file preserving extension (ffmpeg needs it for format detection).
	ext := filepath.Ext(header.Filename)
	tmp, err := os.CreateTemp("", "gleann-plugin-sound-*"+ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create temp file: %v", err))
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("write temp file: %v", err))
		return
	}
	tmp.Close()

	// Get engine (lazy load on first request).
	engine, err := s.getEngine()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load model: %v", err))
		return
	}

	// Transcribe — hold the mutex so concurrent requests are serialised
	// (whisper engine is NOT thread-safe).
	s.mu.Lock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	segments, err := engine.TranscribeFile(ctx, tmpPath)
	s.mu.Unlock()

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("transcription failed: %v", err))
		return
	}

	// Format segments as markdown for the RAG index.
	md := formatMarkdown(header.Filename, segments)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"markdown": md,
	})
}

// formatMarkdown converts transcription segments into a readable markdown document.
func formatMarkdown(filename string, segments []core.Segment) string {
	if len(segments) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Transcription: %s\n\n", filename))

	var fullText []string
	for _, seg := range segments {
		start := formatTimestamp(seg.Start)
		end := formatTimestamp(seg.End)
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		b.WriteString(fmt.Sprintf("[%s - %s] %s\n\n", start, end, text))
		fullText = append(fullText, text)
	}

	// Append a full-text block for better semantic chunking.
	if len(fullText) > 0 {
		b.WriteString("---\n\n")
		b.WriteString(strings.Join(fullText, " "))
		b.WriteString("\n")
	}

	return b.String()
}

// formatTimestamp converts a duration to MM:SS format.
func formatTimestamp(d time.Duration) string {
	total := int(d.Seconds())
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
