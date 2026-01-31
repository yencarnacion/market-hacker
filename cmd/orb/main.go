package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"massive-orb/internal/config"
	"massive-orb/internal/engine"
	"massive-orb/internal/openai"
	"massive-orb/internal/server"
	"massive-orb/internal/store"
	"massive-orb/internal/watchlist"
)

func main() {
	var (
		configPath    = flag.String("config", "config.yaml", "Path to config.yaml")
		watchlistPath = flag.String("watchlist", "watchlist.yaml", "Path to watchlist.yaml")
		historic      = flag.Bool("historic", false, "Run today's session in historic mode (REST replay, no audio)")
	)
	flag.Parse()

	_ = godotenv.Load() // ok if missing; you said assume .env exists

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	wl, err := watchlist.Load(*watchlistPath)
	if err != nil {
		log.Fatalf("failed to load watchlist: %v", err)
	}
	if len(wl) == 0 {
		log.Fatalf("watchlist is empty")
	}

	openaiKey := os.Getenv("OPENAI_API_KEY")
	massiveKey := os.Getenv("MASSIVE_API_KEY")
	if massiveKey == "" {
		log.Fatalf("MASSIVE_API_KEY is missing")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := store.New(cfg, wl)
	if *historic {
		st.SetMode(store.ModeHistoric)
	} else {
		st.SetMode(store.ModeRealtime)
	}

	// IMPORTANT:
	// - In historic mode, never generate audio, even if OPENAI_API_KEY is set.
	var tts *openai.TTSClient
	if *historic {
		tts = nil
		log.Printf("Historic mode enabled: audio disabled; replaying today's session via REST.")
	} else {
		if openaiKey == "" {
			log.Printf("WARN: OPENAI_API_KEY is missing; alerts will be text-only (no audio)")
		}
		tts = openai.NewTTSClient(openaiKey, cfg.OpenAI.TTSModel, cfg.OpenAI.Voice, cfg.OpenAI.ResponseFormat)
	}

	eng := engine.New(cfg, st, massiveKey, tts)
	srv := server.New(cfg, st)

	go func() {
		var runErr error
		if *historic {
			runErr = eng.RunHistoric(ctx)
		} else {
			runErr = eng.Run(ctx)
		}
		if runErr != nil {
			log.Printf("engine stopped with error: %v", runErr)
			stop()
		}
	}()

	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		log.Printf("Server started. Web client is at http://%s", addr)
		if err := srv.Run(ctx); err != nil {
			log.Printf("http server stopped with error: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Printf("Shutting down...")

	// give goroutines a moment to exit cleanly
	time.Sleep(250 * time.Millisecond)
}
