package store

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"massive-orb/internal/config"
)

type Phase string

const (
	PhaseWaitingOpen   Phase = "waiting_open"
	PhaseCollecting5m  Phase = "collecting_open_5m"
	PhaseSelecting0935 Phase = "selecting_0935"
	PhaseTrackingTicks Phase = "tracking_ticks"
	PhaseClosed        Phase = "closed"
)

type Mode string

const (
	ModeRealtime Mode = "realtime"
	ModeHistoric Mode = "historic"
)

// RuntimeFilters are editable at runtime via the web UI.
// Initialized from config.yaml at startup, then mutable.
type RuntimeFilters struct {
	Open5mRangePctMin float64 `json:"open_5m_range_pct_min"`
	Open5mRangePctMax float64 `json:"open_5m_range_pct_max"`
	Open5mVolMin      float64 `json:"open_5m_vol_min"`
	Open5mVolMax      float64 `json:"open_5m_vol_max"`

	Open5mTodayPctMin float64 `json:"open_5m_today_pct_min"`
	Open5mTodayPctMax float64 `json:"open_5m_today_pct_max"`

	EntryMinAfterOpen int     `json:"entry_minutes_after_open_min"`
	EntryMaxAfterOpen int     `json:"entry_minutes_after_open_max"`
	EntryPriceMin     float64 `json:"entry_price_min"`
	EntryPriceMax     float64 `json:"entry_price_max"`
}

type HistoricSummary struct {
	DateNY        string  `json:"date_ny"`
	WindowStartNY string  `json:"window_start_ny"`
	WindowEndNY   string  `json:"window_end_ny"`
	Shares        int     `json:"shares"`
	Candidates    int     `json:"candidates"`
	TradesTaken   int     `json:"trades_taken"`
	NoEntry       int     `json:"no_entry"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	TimeExits     int     `json:"time_exits"`
	WinRate       float64 `json:"win_rate"`
	NetPnL        float64 `json:"net_pnl"`
	TotalNotional float64 `json:"total_notional"`
	NetReturnPct  float64 `json:"net_return_pct"`
	AvgReturnPct  float64 `json:"avg_return_pct"`
	AvgWinPct     float64 `json:"avg_win_pct"`
	AvgLossPct    float64 `json:"avg_loss_pct"`
	ProfitFactor  float64 `json:"profit_factor"`
	BestTradePct  float64 `json:"best_trade_pct"`
	WorstTradePct float64 `json:"worst_trade_pct"`
}

type HistoricTrade struct {
	Symbol                string  `json:"symbol"`
	EntryTimeNY           string  `json:"entry_time_ny"`
	EntryPrice            float64 `json:"entry_price"`
	EntryMinutesAfterOpen float64 `json:"entry_minutes_after_open"`

	TakeProfitPrice float64 `json:"take_profit_price"`
	StopPrice       float64 `json:"stop_price"`

	ExitTimeNY           string  `json:"exit_time_ny"`
	ExitPrice            float64 `json:"exit_price"`
	ExitMinutesAfterOpen float64 `json:"exit_minutes_after_open"`
	ExitReason           string  `json:"exit_reason"`

	Shares         int     `json:"shares"`
	RealizedPnLPct float64 `json:"realized_pnl_pct"`
	RealizedPnL    float64 `json:"realized_pnl"`

	// “What if held to cutoff”
	HoldPrice  float64 `json:"hold_price"`
	HoldPnLPct float64 `json:"hold_pnl_pct"`
	HoldPnL    float64 `json:"hold_pnl"`

	// MFE / MAE from entry → window end
	MFEPrice  float64 `json:"mfe_price"`
	MFETimeNY string  `json:"mfe_time_ny"`
	MFEPnLPct float64 `json:"mfe_pnl_pct"`
	MFEPnL    float64 `json:"mfe_pnl"`

	MAEPrice  float64 `json:"mae_price"`
	MAETimeNY string  `json:"mae_time_ny"`
	MAEPnLPct float64 `json:"mae_pnl_pct"`
	MAEPnL    float64 `json:"mae_pnl"`
}

type HistoricNoEntry struct {
	Symbol         string  `json:"symbol"`
	Open5mVol      float64 `json:"open_5m_vol"`
	Open5mRangePct float64 `json:"open_5m_range_pct"`
	Open5mTodayPct float64 `json:"open_5m_today_pct"`

	SawCrossInWindow bool    `json:"saw_cross_in_window"`
	FirstCrossTimeNY string  `json:"first_cross_time_ny"`
	FirstCrossPrice  float64 `json:"first_cross_price"`

	Reason string `json:"reason"`
}

type HistoricReport struct {
	Summary   HistoricSummary   `json:"summary"`
	Trades    []HistoricTrade   `json:"trades"`
	NoEntries []HistoricNoEntry `json:"no_entries"`
}

type Event struct {
	ID      string `json:"id"`
	TimeNY  string `json:"time_ny"`
	Type    string `json:"type"`
	Symbol  string `json:"symbol,omitempty"`
	Message string `json:"message"`
	AudioID string `json:"audio_id,omitempty"`
	Level   string `json:"level,omitempty"`
}

type PublicTicker struct {
	Symbol            string  `json:"symbol"`
	Open0930          float64 `json:"open_0930"`
	Open5mVol         float64 `json:"open_5m_vol"`
	Open5mRangePct    float64 `json:"open_5m_range_pct"`
	Prev10AvgOpen5m   float64 `json:"prev10_avg_open5m_vol"`
	Open5mTodayPct    float64 `json:"open_5m_today_pct"`

	SawCrossInWindow  bool    `json:"saw_cross_in_window"`
	FirstCrossTimeNY  string  `json:"first_cross_time_ny,omitempty"`
	FirstCrossPrice   float64 `json:"first_cross_price"`
	VWAP              float64 `json:"vwap"`
	LastPrice         float64 `json:"last_price"`
	MinutesAfterOpen  float64 `json:"minutes_after_open"`
	Status            string  `json:"status"`
	EntryPrice        float64 `json:"entry_price"`
	TakeProfitPrice   float64 `json:"take_profit_price"`
	StopPrice         float64 `json:"stop_price"`
}

type TickerState struct {
	Symbol string

	// open-5m metrics (09:30-09:34)
	Open0930 float64
	// true if Open0930 was estimated from the first minute bar we saw (when starting after 09:30)
	Open0930Estimated bool
	ORHigh   float64
	ORLow    float64
	Open5mVol float64
	Open5mRangePct float64

	// history metric at/after 09:35
	Prev10AvgOpen5mVol float64
	Open5mTodayPct     float64

	// tick tracking
	CumPV float64
	CumV  float64
	VWAP  float64

	LastPrice float64
	LastTrade time.Time

	PrevPrice float64
	PrevVWAP  float64

	MinutesAfterOpen float64

	// trade lifecycle
	HasPosition      bool
	EntryPrice       float64
	EntryTime        time.Time
	TakeProfitPrice  float64
	StopPrice        float64
	Exited           bool
	ExitReason       string

	// entry/exit details for reporting
	EntryMinutesAfterOpen float64
	ExitPrice             float64
	ExitTime              time.Time
	ExitMinutesAfterOpen  float64

	// max favorable/adverse excursion tracking from entry → end of session window
	MaxPriceSinceEntry     float64
	MaxPriceSinceEntryTime time.Time
	MinPriceSinceEntry     float64
	MinPriceSinceEntryTime time.Time

	// diagnostics for “selected but no entry”
	SawCrossInWindow bool
	FirstCrossTime   time.Time
	FirstCrossPrice  float64

	Status string // UI-friendly badge
}

type Store struct {
	cfg config.Config

	mode Mode
	historicReport *HistoricReport

	filters RuntimeFilters

	// increments/changes whenever we start a new historic replay so the UI can reset
	sessionID string
	historicTargetDateNY   time.Time
	historicResolvedDateNY time.Time
	historicNote           string

	mu sync.RWMutex

	phase Phase

	watchlist []string
	watchset  map[string]struct{}

	openTimeNY      time.Time
	selectionTimeNY time.Time
	vwapCutoffNY    time.Time
	forceExitNY     time.Time

	tickers map[string]*TickerState

	events []Event
	audio  map[string][]byte
}

type Snapshot struct {
	NowNY           string        `json:"now_ny"`
	Mode            Mode          `json:"mode"`
	Phase           Phase         `json:"phase"`
	WatchlistCount  int           `json:"watchlist_count"`
	OpenTimeNY      string        `json:"open_time_ny"`
	SelectionTimeNY string        `json:"selection_time_ny"`
	VwapCutoffNY    string        `json:"vwap_cutoff_ny"`
	ForceExitNY     string        `json:"force_exit_ny"`
	TrackedCount    int           `json:"tracked_count"`
	Tickers         []PublicTicker `json:"tickers"`
	Filters         RuntimeFilters `json:"filters"`
	Events          []Event       `json:"events"`
	HistoricReport  *HistoricReport `json:"historic_report,omitempty"`

	SessionID              string `json:"session_id"`
	HistoricTargetDateNY   string `json:"historic_target_date_ny,omitempty"`
	HistoricResolvedDateNY string `json:"historic_resolved_date_ny,omitempty"`
	HistoricNote           string `json:"historic_note,omitempty"`
	HistoricMinDateNY      string `json:"historic_min_date_ny,omitempty"`
	HistoricMaxDateNY      string `json:"historic_max_date_ny,omitempty"`
}

func runtimeFiltersFromConfig(cfg config.Config) RuntimeFilters {
	return RuntimeFilters{
		Open5mRangePctMin: cfg.Filters.Open5mRangePctMin,
		Open5mRangePctMax: cfg.Filters.Open5mRangePctMax,
		Open5mVolMin:      cfg.Filters.Open5mVolMin,
		Open5mVolMax:      cfg.Filters.Open5mVolMax,
		Open5mTodayPctMin: cfg.Filters.Open5mTodayPctMin,
		Open5mTodayPctMax: cfg.Filters.Open5mTodayPctMax,
		EntryMinAfterOpen: cfg.Filters.EntryMinAfterOpen,
		EntryMaxAfterOpen: cfg.Filters.EntryMaxAfterOpen,
		EntryPriceMin:     cfg.Filters.EntryPriceMin,
		EntryPriceMax:     cfg.Filters.EntryPriceMax,
	}
}

func validateRuntimeFilters(f RuntimeFilters) error {
	if f.Open5mRangePctMin <= 0 || f.Open5mRangePctMax <= 0 || f.Open5mRangePctMax < f.Open5mRangePctMin {
		return fmt.Errorf("open_5m_range_pct_min/max invalid")
	}
	if f.Open5mVolMax < f.Open5mVolMin {
		return fmt.Errorf("open_5m_vol_min/max invalid")
	}
	if f.Open5mTodayPctMax < f.Open5mTodayPctMin {
		return fmt.Errorf("open_5m_today_pct_min/max invalid")
	}
	if f.EntryMaxAfterOpen < f.EntryMinAfterOpen {
		return fmt.Errorf("entry_minutes_after_open_min/max invalid")
	}
	if f.EntryPriceMax < f.EntryPriceMin {
		return fmt.Errorf("entry_price_min/max invalid")
	}
	return nil
}

func New(cfg config.Config, watchlist []string) *Store {
	ws := make(map[string]struct{}, len(watchlist))
	for _, s := range watchlist {
		ws[s] = struct{}{}
	}
	return &Store{
		cfg:       cfg,
		mode:      ModeRealtime,
		filters:   runtimeFiltersFromConfig(cfg),
		sessionID: strconv.FormatInt(time.Now().UnixNano(), 10),
		phase:     PhaseWaitingOpen,
		watchlist: watchlist,
		watchset:  ws,
		tickers:   make(map[string]*TickerState, 64),
		events:    make([]Event, 0, cfg.UI.MaxEvents),
		audio:     make(map[string][]byte, 256),
	}
}

func (s *Store) Config() config.Config { return s.cfg }

func (s *Store) SetMode(m Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m
}

func (s *Store) Mode() Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *Store) Filters() RuntimeFilters {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filters
}

// UpdateFilters applies a patch function atomically with validation.
func (s *Store) UpdateFilters(fn func(f *RuntimeFilters) error) (RuntimeFilters, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur := s.filters
	next := cur
	if err := fn(&next); err != nil {
		return cur, err
	}
	if err := validateRuntimeFilters(next); err != nil {
		return cur, err
	}
	s.filters = next
	return next, nil
}

func (s *Store) SetHistoricReport(r *HistoricReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historicReport = r
}

// ResetForHistoricRun clears volatile session state (tickers, events, report, audio)
// and updates UI-facing historic metadata. Intended to be called at the start of each historic replay.
func (s *Store) ResetForHistoricRun(targetDateNY, resolvedDateNY time.Time, note string) (sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessionID = strconv.FormatInt(time.Now().UnixNano(), 10)

	s.historicTargetDateNY = targetDateNY
	s.historicResolvedDateNY = resolvedDateNY
	s.historicNote = note

	s.historicReport = nil
	s.tickers = make(map[string]*TickerState, 64)
	s.events = make([]Event, 0, s.cfg.UI.MaxEvents)
	s.audio = make(map[string][]byte, 256)

	return s.sessionID
}

func (s *Store) TickerStates() []TickerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TickerState, 0, len(s.tickers))
	for _, t := range s.tickers {
		out = append(out, *t)
	}
	return out
}

func (s *Store) Watchlist() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.watchlist))
	copy(out, s.watchlist)
	return out
}

func (s *Store) HasSymbol(sym string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.watchset[sym]
	return ok
}

func (s *Store) SetTimes(openNY, selNY, cutoffNY, exitNY time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openTimeNY = openNY
	s.selectionTimeNY = selNY
	s.vwapCutoffNY = cutoffNY
	s.forceExitNY = exitNY
}

func (s *Store) Times() (openNY, selNY, cutoffNY, exitNY time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.openTimeNY, s.selectionTimeNY, s.vwapCutoffNY, s.forceExitNY
}

func (s *Store) SetPhase(p Phase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = p
}

func (s *Store) Phase() Phase {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phase
}

func (s *Store) UpsertTicker(sym string, fn func(t *TickerState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickers[sym]
	if !ok {
		t = &TickerState{Symbol: sym, Status: "watching"}
		s.tickers[sym] = t
	}
	fn(t)
}

func (s *Store) GetTicker(sym string) *TickerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tickers[sym]
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func (s *Store) SetTrackedTickers(states map[string]*TickerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickers = states
}

func (s *Store) StoreAudio(audioID string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audio[audioID] = b
}

func (s *Store) GetAudio(audioID string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.audio[audioID]
	return b, ok
}

func (s *Store) AddEvent(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.events) >= s.cfg.UI.MaxEvents {
		// drop oldest
		copy(s.events, s.events[1:])
		s.events[len(s.events)-1] = ev
		return
	}
	s.events = append(s.events, ev)
}

func (s *Store) Snapshot(nowNY time.Time) Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	events := make([]Event, len(s.events))
	copy(events, s.events)

	tickers := make([]PublicTicker, 0, len(s.tickers))
	for _, t := range s.tickers {
		tickers = append(tickers, PublicTicker{
			Symbol:           t.Symbol,
			Open0930:         t.Open0930,
			Open5mVol:        t.Open5mVol,
			Open5mRangePct:   t.Open5mRangePct,
			Prev10AvgOpen5m:  t.Prev10AvgOpen5mVol,
			Open5mTodayPct:   t.Open5mTodayPct,
			SawCrossInWindow: t.SawCrossInWindow,
			FirstCrossTimeNY: func() string {
				if t.FirstCrossTime.IsZero() { return "" }
				return t.FirstCrossTime.In(nowNY.Location()).Format("15:04:05")
			}(),
			FirstCrossPrice:  t.FirstCrossPrice,
			VWAP:             t.VWAP,
			LastPrice:        t.LastPrice,
			MinutesAfterOpen: t.MinutesAfterOpen,
			Status:           t.Status,
			EntryPrice:       t.EntryPrice,
			TakeProfitPrice:  t.TakeProfitPrice,
			StopPrice:        t.StopPrice,
		})
	}

	var rep *HistoricReport
	if s.historicReport != nil {
		cp := *s.historicReport
		cp.Trades = append([]HistoricTrade(nil), s.historicReport.Trades...)
		cp.NoEntries = append([]HistoricNoEntry(nil), s.historicReport.NoEntries...)
		rep = &cp
	}

	// Historic date picker bounds (NY)
	histMin := ""
	histMax := ""
	if s.mode == ModeHistoric {
		day := time.Date(nowNY.Year(), nowNY.Month(), nowNY.Day(), 0, 0, 0, 0, nowNY.Location())
		histMax = day.Format("2006-01-02")
		histMin = day.AddDate(0, 0, -s.cfg.History.MaxCalendarLookback).Format("2006-01-02")
	}

	tgt := ""
	res := ""
	if !s.historicTargetDateNY.IsZero() {
		tgt = s.historicTargetDateNY.Format("2006-01-02")
	}
	if !s.historicResolvedDateNY.IsZero() {
		res = s.historicResolvedDateNY.Format("2006-01-02")
	}

	return Snapshot{
		NowNY:           nowNY.Format("2006-01-02 15:04:05"),
		Mode:            s.mode,
		Phase:           s.phase,
		WatchlistCount:  len(s.watchlist),
		OpenTimeNY:      s.openTimeNY.Format("2006-01-02 15:04:05"),
		SelectionTimeNY: s.selectionTimeNY.Format("2006-01-02 15:04:05"),
		VwapCutoffNY:    s.vwapCutoffNY.Format("2006-01-02 15:04:05"),
		ForceExitNY:     s.forceExitNY.Format("2006-01-02 15:04:05"),
		TrackedCount:    len(s.tickers),
		Tickers:         tickers,
		Filters:         s.filters,
		Events:          events,
		HistoricReport:  rep,

		SessionID:              s.sessionID,
		HistoricTargetDateNY:   tgt,
		HistoricResolvedDateNY: res,
		HistoricNote:           s.historicNote,
		HistoricMinDateNY:      histMin,
		HistoricMaxDateNY:      histMax,
	}
}
