package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
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
	mux.HandleFunc("/api/filters", s.handleFilters)

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

type filtersPatch struct {
	Open5mRangePctMin *float64 `json:"open_5m_range_pct_min,omitempty"`
	Open5mRangePctMax *float64 `json:"open_5m_range_pct_max,omitempty"`
	Open5mVolMin      *float64 `json:"open_5m_vol_min,omitempty"`
	Open5mVolMax      *float64 `json:"open_5m_vol_max,omitempty"`

	Open5mTodayPctMin *float64 `json:"open_5m_today_pct_min,omitempty"`
	Open5mTodayPctMax *float64 `json:"open_5m_today_pct_max,omitempty"`

	EntryMinAfterOpen *int     `json:"entry_minutes_after_open_min,omitempty"`
	EntryMaxAfterOpen *int     `json:"entry_minutes_after_open_max,omitempty"`
	EntryPriceMin     *float64 `json:"entry_price_min,omitempty"`
	EntryPriceMax     *float64 `json:"entry_price_max,omitempty"`
}

func (s *Server) handleFilters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		f := s.st.Filters()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "filters": f})
		return

	case http.MethodPost:
		var p filtersPatch
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid json body"})
			return
		}

		updated, err := s.st.UpdateFilters(func(f *store.RuntimeFilters) error {
			if p.Open5mRangePctMin != nil {
				f.Open5mRangePctMin = *p.Open5mRangePctMin
			}
			if p.Open5mRangePctMax != nil {
				f.Open5mRangePctMax = *p.Open5mRangePctMax
			}
			if p.Open5mVolMin != nil {
				f.Open5mVolMin = *p.Open5mVolMin
			}
			if p.Open5mVolMax != nil {
				f.Open5mVolMax = *p.Open5mVolMax
			}
			if p.Open5mTodayPctMin != nil {
				f.Open5mTodayPctMin = *p.Open5mTodayPctMin
			}
			if p.Open5mTodayPctMax != nil {
				f.Open5mTodayPctMax = *p.Open5mTodayPctMax
			}
			if p.EntryMinAfterOpen != nil {
				f.EntryMinAfterOpen = *p.EntryMinAfterOpen
			}
			if p.EntryMaxAfterOpen != nil {
				f.EntryMaxAfterOpen = *p.EntryMaxAfterOpen
			}
			if p.EntryPriceMin != nil {
				f.EntryPriceMin = *p.EntryPriceMin
			}
			if p.EntryPriceMax != nil {
				f.EntryPriceMax = *p.EntryPriceMax
			}
			return nil
		})
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}

		// Emit an event so it shows up in the log.
		loc := mustLoc(s.cfg.Market.Timezone)
		nowNY := time.Now().In(loc)
		msg := fmt.Sprintf(
			"Filters updated. OR rng=%.3f–%.3f, OR vol=%.0f–%.0f, Today%%=%.0f–%.0f, EntryMin=%d–%d, Px=%.2f–%.2f",
			updated.Open5mRangePctMin, updated.Open5mRangePctMax,
			updated.Open5mVolMin, updated.Open5mVolMax,
			updated.Open5mTodayPctMin, updated.Open5mTodayPctMax,
			updated.EntryMinAfterOpen, updated.EntryMaxAfterOpen,
			updated.EntryPriceMin, updated.EntryPriceMax,
		)
		s.st.AddEvent(store.Event{
			ID:      fmt.Sprintf("%d-FILTERS", nowNY.UnixNano()),
			TimeNY:  nowNY.Format("15:04:05"),
			Type:    "FILTERS",
			Message: msg,
			Level:   "info",
		})

		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "filters": updated})
		return

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
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
