package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/httpserver"
)

// newPluginServeCmd creates the "plugin-serve" subcommand.
//
// This runs an HTTP server that conforms to the gleann PluginManager contract
// (GET /health, POST /convert) so that gleann build can automatically
// transcribe audio/video files during index construction.
//
// Usage:
//
//	gleann-plugin-sound plugin-serve --port 8766
func newPluginServeCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "plugin-serve",
		Short: "Run HTTP server for gleann plugin integration (document extraction)",
		Long: `Starts an HTTP server that integrates with the gleann PluginManager.

When gleann builds an index and encounters audio/video files (.mp3, .wav, etc.),
it sends them to this server for transcription. The server runs Whisper inference
and returns the transcribed text as markdown.

The model is loaded lazily on the first request to keep startup fast.
Register this plugin with 'gleann-plugin-sound install' before use.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			backend, _ := cmd.Flags().GetString("backend")
			language, _ := cmd.Flags().GetString("language")

			// Read language from config if not specified via flag.
			if language == "" {
				if cfg := config.Load(); cfg != nil && cfg.Language != "" {
					language = cfg.Language
				}
			}

			// Engine factory — creates the transcriber on first /convert request.
			factory := func() (core.Transcriber, error) {
				log.Printf("[plugin-serve] creating transcriber: backend=%s model=%s", backend, modelPath)
				return core.NewTranscriber(backend, modelPath)
			}

			srv := httpserver.New(factory, language, port)
			defer srv.Close()

			// Graceful shutdown on SIGINT/SIGTERM.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Println("[plugin-serve] shutting down...")
				srv.Close()
				os.Exit(0)
			}()

			return srv.Serve()
		},
	}

	cmd.Flags().IntVar(&port, "port", 8766, "HTTP port for the plugin server")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en). Empty = auto-detect")

	return cmd
}

