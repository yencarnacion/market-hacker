package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"massive-orb/internal/config"
	"massive-orb/internal/engine"
	"massive-orb/internal/store"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	cfg config.Config
	st  *store.Store
	hub *SSEHub

	eng      *engine.Engine
	histReqC chan time.Time
}

func New(cfg config.Config, st *store.Store, eng *engine.Engine) *Server {
	return &Server{
		cfg: cfg,
		st:  st,
		hub: NewSSEHub(),
		eng: eng,
		histReqC: make(chan time.Time, 1),
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
	mux.HandleFunc("/api/historic/run", s.handleHistoricRun)

	// Push events to SSE hub by polling store’s event list:
	// (simple + robust; if you want lower latency, you can refactor Store.AddEvent to call hub.Broadcast directly)
	go s.eventPump(ctx)

	// Historic runner loop (only meaningful in historic mode)
	if s.st.Mode() == store.ModeHistoric {
		go s.historicRunLoop(ctx)
	}

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

// QueueHistoricRun enqueues a historic run request; if one is already queued, it replaces it.
func (s *Server) QueueHistoricRun(targetDateNY time.Time) {
	select {
	case s.histReqC <- targetDateNY:
	default:
		// replace pending request with newest
		select {
		case <-s.histReqC:
		default:
		}
		s.histReqC <- targetDateNY
	}
}

func (s *Server) historicRunLoop(ctx context.Context) {
	var (
		cancel context.CancelFunc
		done   chan struct{}
	)

	for {
		select {
		case <-ctx.Done():
			if cancel != nil {
				cancel()
			}
			if done != nil {
				<-done
			}
			return

		case target := <-s.histReqC:
			if s.eng == nil {
				log.Printf("historic run requested but engine is nil")
				continue
			}
			if s.st.Mode() != store.ModeHistoric {
				continue
			}

			// cancel the current run (if any) and wait for it to fully exit
			if cancel != nil {
				cancel()
				if done != nil {
					<-done
				}
			}

			runCtx, c := context.WithCancel(ctx)
			cancel = c
			done = make(chan struct{})

			go func(runCtx context.Context, d time.Time, done chan struct{}) {
				defer close(done)
				if err := s.eng.RunHistoricForDate(runCtx, d); err != nil {
					log.Printf("historic run error: %v", err)
				}
			}(runCtx, target, done)
		}
	}
}

func (s *Server) handleHistoricRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.st.Mode() != store.ModeHistoric {
		http.Error(w, "not in historic mode", http.StatusConflict)
		return
	}

	loc := mustLoc(s.cfg.Market.Timezone)

	dateStr := r.URL.Query().Get("date") // YYYY-MM-DD
	var target time.Time
	if dateStr == "" {
		target = time.Now().In(loc)
	} else {
		t, err := time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			http.Error(w, "invalid date (use YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		target = t
	}

	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	reqDay := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, loc)

	if reqDay.After(today) {
		http.Error(w, "date cannot be in the future", http.StatusBadRequest)
		return
	}
	min := today.AddDate(0, 0, -s.cfg.History.MaxCalendarLookback)
	if reqDay.Before(min) {
		http.Error(w, "date is older than configured max_calendar_lookback_days", http.StatusBadRequest)
		return
	}

	s.QueueHistoricRun(reqDay)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":                 true,
		"requested_date_ny":   reqDay.Format("2006-01-02"),
	})
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
