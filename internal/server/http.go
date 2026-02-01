package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/massive-com/client-go/v2/rest/models"

	"massive-orb/internal/config"
	"massive-orb/internal/engine"
	"massive-orb/internal/massive"
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
		cfg:      cfg,
		st:       st,
		hub:      NewSSEHub(),
		eng:      eng,
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

	// NEW: chart bars for the “Of interest” slideshow
	mux.HandleFunc("/api/chart/bars", s.handleChartBars)

	// Push events to SSE hub by polling store’s event list:
	go s.eventPump(ctx)

	// Historic runner loop (only meaningful in historic mode)
	if s.st.Mode() == store.ModeHistoric {
		go s.historicRunLoop(ctx)
	}

	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	return srv.ListenAndServe()
}

// ---- NEW: chart bars endpoint ----

type chartBar struct {
	Time   int64   `json:"time"` // unix seconds
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

type chartBarsResp struct {
	OK              bool       `json:"ok"`
	Symbol          string     `json:"symbol"`
	DateNY          string     `json:"date_ny"`
	Timezone        string     `json:"timezone"`
	OpenUnix        int64      `json:"open_unix"` // 09:30 NY in unix seconds (VWAP anchor)
	ExitUnix        int64      `json:"exit_unix"` // 11:00 NY in unix seconds (force-exit / "show rest" end)
	PrevCloseDateNY string     `json:"prev_close_date_ny,omitempty"`
	Bars            []chartBar `json:"bars"`
	Error           string     `json:"error,omitempty"`
}

func (s *Server) handleChartBars(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sym := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if sym == "" {
		http.Error(w, "missing symbol", http.StatusBadRequest)
		return
	}

	loc := mustLoc(s.cfg.Market.Timezone)

	dateStr := strings.TrimSpace(r.URL.Query().Get("date")) // YYYY-MM-DD
	var dayNY time.Time
	if dateStr == "" {
		dayNY = time.Now().In(loc)
	} else {
		t, err := time.ParseInLocation("2006-01-02", dateStr, loc)
		if err != nil {
			http.Error(w, "invalid date (use YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		dayNY = t
	}
	dayNY = time.Date(dayNY.Year(), dayNY.Month(), dayNY.Day(), 0, 0, 0, 0, loc)

	openNY := atTimeInLoc(dayNY, s.cfg.Market.OpenTime, loc)      // usually 09:30:00
	exitNY := atTimeInLoc(dayNY, s.cfg.Market.ForceExitTime, loc) // usually 11:00:00

	// Request 09:30 → 11:00 (range is [from,to))
	endNY := exitNY

	key := os.Getenv("MASSIVE_API_KEY")
	if key == "" {
		http.Error(w, "MASSIVE_API_KEY is missing on server", http.StatusInternalServerError)
		return
	}
	rest := massive.NewREST(key)

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	// Local helper: fetch 1m aggs and assign timestamps by index (start + i*1m).
	listBars := func(startNY, endNY time.Time) ([]chartBar, error) {
		params := models.ListAggsParams{
			Ticker:     sym,
			Multiplier: 1,
			Timespan:   models.Minute,
			From:       massive.ToMillis(startNY),
			To:         massive.ToMillis(endNY),
		}
		it := rest.ListAggs(ctx, &params)

		out := make([]chartBar, 0, 64)
		idx := 0
		for it.Next() {
			a := it.Item()
			t := startNY.Add(time.Duration(idx) * time.Minute)
			idx++

			// Skip clearly-bad bars but keep the idx progression stable.
			if a.Open <= 0 || a.High <= 0 || a.Low <= 0 || a.Close <= 0 {
				continue
			}
			out = append(out, chartBar{
				Time:   t.Unix(),
				Open:   a.Open,
				High:   a.High,
				Low:    a.Low,
				Close:  a.Close,
				Volume: float64(a.Volume),
			})
		}
		if err := it.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}

	// Find previous session “last minute” bar (15:59→16:00) by searching backwards.
	prevCloseDate := ""
	var prevBar chartBar
	gotPrev := false

	d := dayNY.AddDate(0, 0, -1)
	for tries := 0; tries < 15; tries++ {
		// skip weekends quickly
		for d.Weekday() == time.Saturday {
			d = d.AddDate(0, 0, -1)
		}
		for d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, -1)
		}

		closeStart := time.Date(d.Year(), d.Month(), d.Day(), 15, 59, 0, 0, loc)
		closeEnd := closeStart.Add(1 * time.Minute)

		bs, err := listBars(closeStart, closeEnd)
		if err == nil && len(bs) > 0 {
			prevBar = bs[len(bs)-1]
			prevCloseDate = d.Format("2006-01-02")
			gotPrev = true
			break
		}

		d = d.AddDate(0, 0, -1)
	}

	// Today session bars
	todayBars, err := listBars(openNY, endNY)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(chartBarsResp{
			OK:       false,
			Symbol:   sym,
			DateNY:   dayNY.Format("2006-01-02"),
			Timezone: s.cfg.Market.Timezone,
			OpenUnix: openNY.Unix(),
			ExitUnix: exitNY.Unix(),
			Error:    fmt.Sprintf("failed to fetch bars: %v", err),
		})
		return
	}

	// Build final bar list:
	// - Put prev close bar at 09:29 (synthetic placement so it appears “right before open”)
	// - Then 09:30.. bars
	out := make([]chartBar, 0, 1+len(todayBars))
	if gotPrev {
		prevBar.Time = openNY.Add(-1 * time.Minute).Unix() // 09:29
		out = append(out, prevBar)
	}
	out = append(out, todayBars...)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chartBarsResp{
		OK:              true,
		Symbol:          sym,
		DateNY:          dayNY.Format("2006-01-02"),
		Timezone:        s.cfg.Market.Timezone,
		OpenUnix:        openNY.Unix(),
		ExitUnix:        exitNY.Unix(),
		PrevCloseDateNY: prevCloseDate,
		Bars:            out,
	})
}

// ---- existing code below (unchanged) ----
// (keep the rest of your file as-is)

// helper: parse "HH:MM:SS" and return a time on the same date in loc
func atTimeInLoc(day time.Time, hms string, loc *time.Location) time.Time {
	var hh, mm, ss int
	_, _ = fmt.Sscanf(hms, "%d:%d:%d", &hh, &mm, &ss)
	d := day.In(loc)
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, 0, loc)
}
