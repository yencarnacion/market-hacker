package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"massive-orb/internal/config"
	"massive-orb/internal/store"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	cfg config.Config
	st  *store.Store
	hub *SSEHub
}

func New(cfg config.Config, st *store.Store) *Server {
	return &Server{
		cfg: cfg,
		st:  st,
		hub: NewSSEHub(),
	}
}

func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Static site
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// API
	mux.HandleFunc("/api/state", s.handleState)
	mux.Handle("/api/events", s.hub)
	mux.HandleFunc("/api/audio/", s.handleAudio)

	// Push events to SSE hub by polling store’s event list:
	// (simple + robust; if you want lower latency, you can refactor Store.AddEvent to call hub.Broadcast directly)
	go s.eventPump(ctx)

	srv := &http.Server{
		Addr:    s.cfg.Server.Host + ":" + itoa(s.cfg.Server.Port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	return srv.ListenAndServe()
}

func (s *Server) eventPump(ctx context.Context) {
	// naive: broadcast full newest event list tail every 250ms
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()

	var lastID string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			snap := s.st.Snapshot(time.Now().In(mustLoc(s.cfg.Market.Timezone)))
			if len(snap.Events) == 0 {
				continue
			}
			ev := snap.Events[len(snap.Events)-1]
			if ev.ID == lastID {
				continue
			}
			lastID = ev.ID
			s.hub.Broadcast(ev)
		}
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	loc := mustLoc(s.cfg.Market.Timezone)
	snap := s.st.Snapshot(time.Now().In(loc))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	// /api/audio/{id}.mp3
	id := strings.TrimPrefix(r.URL.Path, "/api/audio/")
	id = strings.TrimSuffix(id, ".mp3")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	b, ok := s.st.GetAudio(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func mustLoc(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone("UTC", 0)
	}
	return loc
}

func itoa(n int) string {
	// tiny, dependency-free
	if n == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + (n % 10))
		n /= 10
	}
	return string(buf[i:])
}
