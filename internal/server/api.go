package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"massive-orb/internal/store"
)

// ---------- /api/state ----------

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	loc := mustLoc(s.cfg.Market.Timezone)
	nowNY := time.Now().In(loc)

	// If times haven't been initialized yet (e.g. engine not started), set them once.
	openNY, selNY, cutoffNY, exitNY := s.st.Times()
	if openNY.IsZero() || selNY.IsZero() || cutoffNY.IsZero() || exitNY.IsZero() {
		dayNY := time.Date(nowNY.Year(), nowNY.Month(), nowNY.Day(), 0, 0, 0, 0, loc)
		openNY = atTimeInLoc(dayNY, s.cfg.Market.OpenTime, loc)
		selNY = atTimeInLoc(dayNY, s.cfg.Market.SelectionTime, loc)
		cutoffNY = atTimeInLoc(dayNY, s.cfg.Market.VWAPCrossCutoff, loc)
		exitNY = atTimeInLoc(dayNY, s.cfg.Market.ForceExitTime, loc)
		s.st.SetTimes(openNY, selNY, cutoffNY, exitNY)
	}

	snap := s.st.Snapshot(nowNY)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// ---------- /api/audio/<id>.mp3 ----------

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/audio/<id>.mp3
	p := strings.TrimPrefix(r.URL.Path, "/api/audio/")
	p = strings.TrimSpace(p)
	if p == "" {
		http.Error(w, "missing audio id", http.StatusBadRequest)
		return
	}
	p = strings.TrimSuffix(p, ".mp3")

	b, ok := s.st.GetAudio(p)
	if !ok || len(b) == 0 {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}

// ---------- /api/filters (GET/POST) ----------

type filtersPatch struct {
	Open5mRangePctMin *float64 `json:"open_5m_range_pct_min"`
	Open5mRangePctMax *float64 `json:"open_5m_range_pct_max"`
	Open5mVolMin      *float64 `json:"open_5m_vol_min"`
	Open5mVolMax      *float64 `json:"open_5m_vol_max"`

	Open5mTodayPctMin *float64 `json:"open_5m_today_pct_min"`
	Open5mTodayPctMax *float64 `json:"open_5m_today_pct_max"`

	EntryMinAfterOpen *int     `json:"entry_minutes_after_open_min"`
	EntryMaxAfterOpen *int     `json:"entry_minutes_after_open_max"`
	EntryPriceMin     *float64 `json:"entry_price_min"`
	EntryPriceMax     *float64 `json:"entry_price_max"`

	SoldOffFromOpenPctMin    *float64 `json:"sold_off_from_open_pct_min"`
	SoldOffOpen5mRangePctMin *float64 `json:"sold_off_open5m_range_pct_min"`
	SoldOffOpen5mTodayPctMin *float64 `json:"sold_off_open5m_today_pct_min"`
}

func (s *Server) handleFilters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"filters": s.st.Filters(),
		})
		return

	case http.MethodPost:
		var p filtersPatch
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}

		next, err := s.st.UpdateFilters(func(f *store.RuntimeFilters) error {
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

			if p.SoldOffFromOpenPctMin != nil {
				f.SoldOffFromOpenPctMin = *p.SoldOffFromOpenPctMin
			}
			if p.SoldOffOpen5mRangePctMin != nil {
				f.SoldOffOpen5mRangePctMin = *p.SoldOffOpen5mRangePctMin
			}
			if p.SoldOffOpen5mTodayPctMin != nil {
				f.SoldOffOpen5mTodayPctMin = *p.SoldOffOpen5mTodayPctMin
			}
			return nil
		})
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"filters": next,
		})
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

// ---------- /api/historic/run?date=YYYY-MM-DD ----------

func (s *Server) handleHistoricRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.st.Mode() != store.ModeHistoric {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "not in historic mode",
		})
		return
	}

	dateISO := strings.TrimSpace(r.URL.Query().Get("date"))
	if dateISO == "" {
		http.Error(w, "missing date", http.StatusBadRequest)
		return
	}

	loc := mustLoc(s.cfg.Market.Timezone)
	dayNY, err := time.ParseInLocation("2006-01-02", dateISO, loc)
	if err != nil {
		http.Error(w, "invalid date (use YYYY-MM-DD)", http.StatusBadRequest)
		return
	}

	s.QueueHistoricRun(dayNY)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// ---------- SSE event pump ----------

func (s *Server) eventPump(ctx context.Context) {
	loc := mustLoc(s.cfg.Market.Timezone)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	lastSession := ""
	lastLen := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := s.st.Snapshot(time.Now().In(loc))

			// new session boundary
			if snap.SessionID != "" && snap.SessionID != lastSession {
				lastSession = snap.SessionID
				lastLen = 0
			}

			evs := snap.Events
			if len(evs) < lastLen {
				// list was reset/truncated
				lastLen = 0
			}
			for i := lastLen; i < len(evs); i++ {
				s.hub.Broadcast(evs[i])
			}
			lastLen = len(evs)
		}
	}
}

// ---------- Historic run queue + loop ----------

func (s *Server) QueueHistoricRun(dayNY time.Time) {
	// non-blocking "latest wins"
	select {
	case s.histReqC <- dayNY:
		return
	default:
		// channel full: drop the old request and replace it
		select {
		case <-s.histReqC:
		default:
		}
		select {
		case s.histReqC <- dayNY:
		default:
		}
	}
}

func (s *Server) historicRunLoop(ctx context.Context) {
	// One run at a time; if a new request comes in, cancel current run,
	// wait for it to exit, then run the newest requested date.
	loc := mustLoc(s.cfg.Market.Timezone)

	var (
		running bool
		cancel  context.CancelFunc
		doneCh  chan struct{}
	)

	startRun := func(day time.Time) {
		runCtx, c := context.WithCancel(ctx)
		cancel = c
		doneCh = make(chan struct{})
		running = true

		go func(day time.Time) {
			defer close(doneCh)

			// normalize to NY date boundary
			day = time.Date(day.In(loc).Year(), day.In(loc).Month(), day.In(loc).Day(), 0, 0, 0, 0, loc)

			if err := s.eng.RunHistoricForDate(runCtx, day); err != nil && runCtx.Err() == nil {
				now := time.Now().In(loc)
				s.st.AddEvent(store.Event{
					ID:      fmt.Sprintf("%d-SYSTEM-historic", now.UnixNano()),
					TimeNY:  now.Format("15:04:05"),
					Type:    "SYSTEM",
					Message: fmt.Sprintf("Historic run failed: %v", err),
					Level:   "warn",
				})
			}
		}(day)
	}

	for {
		select {
		case <-ctx.Done():
			if running && cancel != nil {
				cancel()
				<-doneCh
			}
			return

		case day := <-s.histReqC:
			if s.st.Mode() != store.ModeHistoric {
				continue
			}

			if !running {
				startRun(day)
				continue
			}

			// cancel current
			if cancel != nil {
				cancel()
			}
			<-doneCh
			running = false
			cancel = nil
			doneCh = nil

			// Drain any extra queued requests and keep only the most recent
			latest := day
			for {
				select {
				case d := <-s.histReqC:
					latest = d
				default:
					goto runLatest
				}
			}

		runLatest:
			startRun(latest)
		}
	}
}
