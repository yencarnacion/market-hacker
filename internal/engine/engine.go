package engine

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"massive-orb/internal/config"
	"massive-orb/internal/massive"
	"massive-orb/internal/nato"
	"massive-orb/internal/openai"
	"massive-orb/internal/store"

	massivews "github.com/massive-com/client-go/v2/websocket"
)

type Engine struct {
	cfg       config.Config
	st        *store.Store
	massiveKey string
	tts       *openai.TTSClient

	loc *time.Location
}

func New(cfg config.Config, st *store.Store, massiveKey string, tts *openai.TTSClient) *Engine {
	loc, _ := time.LoadLocation(cfg.Market.Timezone)
	return &Engine{
		cfg:       cfg,
		st:        st,
		massiveKey: massiveKey,
		tts:       tts,
		loc:       loc,
	}
}

func (e *Engine) Run(ctx context.Context) error {
	// Set today's key times in NY
	nowNY := time.Now().In(e.loc)
	openNY := atTime(nowNY, e.cfg.Market.OpenTime, e.loc)
	selNY := atTime(nowNY, e.cfg.Market.SelectionTime, e.loc)
	cutoffNY := atTime(nowNY, e.cfg.Market.VWAPCrossCutoff, e.loc)
	exitNY := atTime(nowNY, e.cfg.Market.ForceExitTime, e.loc)

	e.st.SetTimes(openNY, selNY, cutoffNY, exitNY)

	e.emit(nowNY, "SYSTEM", "", fmt.Sprintf("Loaded watchlist: %d tickers", len(e.st.Watchlist())), "", "info")

	// Wait for open
	if nowNY.Before(openNY) {
		e.st.SetPhase(store.PhaseWaitingOpen)
		e.emit(nowNY, "SYSTEM", "", fmt.Sprintf("Waiting for market open at %s", openNY.Format("15:04:05")), "", "info")
		timer := time.NewTimer(time.Until(openNY))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}

	e.st.SetPhase(store.PhaseCollecting5m)

	// Phase 1: Subscribe to minute aggregates for all watchlist tickers
	wsAgg, err := massive.NewWS(e.massiveKey, e.cfg.Massive.Feed)
	if err != nil {
		return err
	}
	defer wsAgg.Close()

	// Subscribe in batches (important for 8k)
	wl := e.st.Watchlist()
	for i := 0; i < len(wl); i += e.cfg.Massive.WSBatchSize {
		j := i + e.cfg.Massive.WSBatchSize
		if j > len(wl) {
			j = len(wl)
		}
		if err := wsAgg.Subscribe(massivews.StocksMinAggs, wl[i:j]...); err != nil {
			return fmt.Errorf("subscribe minute aggs: %w", err)
		}
	}

	if err := wsAgg.Connect(); err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}

	e.emit(time.Now().In(e.loc), "SYSTEM", "", "Collecting 09:30-09:34 minute bars for open-5m metrics...", "", "info")

	// Collect minute bars until selection time
	done0935 := make(chan struct{})
	go func() {
		defer close(done0935)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-wsAgg.Error():
				if !ok {
					return
				}
				if err != nil {
					e.emit(time.Now().In(e.loc), "SYSTEM", "", fmt.Sprintf("WS error: %v", err), "", "warn")
					return
				}
			case msg, ok := <-wsAgg.Output():
				if !ok {
					return
				}
				agg, ok := msg.(massive.EquityAgg)
				if !ok {
					continue
				}
				e.onMinuteAgg(openNY, selNY, agg)
			}
		}
	}()

	// Wait until 09:35
	if time.Now().In(e.loc).Before(selNY) {
		timer := time.NewTimer(time.Until(selNY))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}

	e.st.SetPhase(store.PhaseSelecting0935)
	wsAgg.Close()
	<-done0935

	// Select candidates
	candidates := e.selectCandidatesAt0935()
	if len(candidates) == 0 {
		e.emit(time.Now().In(e.loc), "SYSTEM", "", "No tickers matched open_5m filters at 09:35.", "", "info")
		e.st.SetPhase(store.PhaseClosed)
		return nil
	}

	e.emit(time.Now().In(e.loc), "SYSTEM", "", fmt.Sprintf("09:35 selection: %d tickers matched opening filters (switching to trades)", len(candidates)), "", "info")

	// Create REST client
	restClient := massive.NewREST(e.massiveKey)

	rest := newRESTShim(restClient)

	tracked, err := e.buildTrackedStates(ctx, rest, openNY, selNY, candidates)

	if err != nil {
		return err
	}
	e.st.SetTrackedTickers(tracked)

	e.st.SetPhase(store.PhaseTrackingTicks)

	// Phase 2: WebSocket trades for tracked tickers only
	wsTrades, err := massive.NewWS(e.massiveKey, e.cfg.Massive.Feed)
	if err != nil {
		return err
	}
	defer wsTrades.Close()

	syms := make([]string, 0, len(tracked))
	for sym := range tracked {
		syms = append(syms, sym)
	}
	sort.Strings(syms)

	if err := wsTrades.Subscribe(massivews.StocksTrades, syms...); err != nil {
		return fmt.Errorf("subscribe trades: %w", err)
	}
	if err := wsTrades.Connect(); err != nil {
		return fmt.Errorf("ws trades connect: %w", err)
	}

	// 11am timer
	closed11am := make(chan struct{})
	go func() {
		defer close(closed11am)
		now := time.Now().In(e.loc)
		if now.Before(exitNY) {
			t := time.NewTimer(time.Until(exitNY))
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
		e.onElevenAM(exitNY)
	}()

	e.emit(time.Now().In(e.loc), "SYSTEM", "", "Tracking tick data (trades) for filtered tickers...", "", "info")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-closed11am:
			e.st.SetPhase(store.PhaseClosed)
			return nil
		case err, ok := <-wsTrades.Error():
			if !ok {
				return nil
			}
			if err != nil {
				return err
			}
		case msg, ok := <-wsTrades.Output():
			if !ok {
				return nil
			}
			tr, ok := msg.(massive.EquityTrade)
			if !ok {
				continue
			}
			e.onTrade(openNY, selNY, cutoffNY, exitNY, tr)
		}
	}
}

// ---- Minute aggregates processing (09:30-09:34) ----
func (e *Engine) onMinuteAgg(openNY, selNY time.Time, agg massive.EquityAgg) {
	sym := agg.Symbol
	if sym == "" {
		return
	}
	if !e.st.HasSymbol(sym) {
		return
	}

	// massive websocket timestamps are in ms; use StartTimestamp to bucket the bar
	barStartNY := time.UnixMilli(agg.StartTimestamp).In(e.loc)

	// Only collect [09:30, 09:35)
	if barStartNY.Before(openNY) || !barStartNY.Before(selNY) {
		return
	}

	hhmm := barStartNY.Format("15:04")
	if hhmm < "09:30" || hhmm > "09:34" {
		return
	}

	e.st.UpsertTicker(sym, func(t *store.TickerState) {
		if hhmm == "09:30" && t.Open0930 == 0 {
			t.Open0930 = agg.Open
			t.ORHigh = agg.High
			t.ORLow = agg.Low
		} else {
			if agg.High > t.ORHigh {
				t.ORHigh = agg.High
			}
			if t.ORLow == 0 || agg.Low < t.ORLow {
				t.ORLow = agg.Low
			}
		}
		t.Open5mVol += agg.Volume
	})
}

func (e *Engine) selectCandidatesAt0935() []string {
	cfg := e.cfg

	nowNY := time.Now().In(e.loc)
	e.emit(nowNY, "SYSTEM", "", "Computing open_5m_range_pct + open_5m_vol and filtering...", "", "info")

	wl := e.st.Watchlist()

	candidates := make([]string, 0, 64)
	for _, sym := range wl {
		t := e.st.GetTicker(sym)
		if t == nil {
			continue
		}
		if t.Open0930 <= 0 || t.ORHigh <= 0 || t.ORLow <= 0 {
			continue
		}
		rng := (t.ORHigh - t.ORLow) / t.Open0930
		vol := t.Open5mVol

		if rng >= cfg.Filters.Open5mRangePctMin && rng <= cfg.Filters.Open5mRangePctMax &&
			vol >= cfg.Filters.Open5mVolMin && vol <= cfg.Filters.Open5mVolMax {

			candidates = append(candidates, sym)

			// store computed open_5m_range_pct back to state
			e.st.UpsertTicker(sym, func(tt *store.TickerState) {
				tt.Open5mRangePct = rng
				tt.Status = "selected"
			})
		}
	}

	sort.Strings(candidates)
	return candidates
}

func (e *Engine) buildTrackedStates(
	ctx context.Context,
	rest *mrestClientShim,
	openNY, selNY time.Time,
	candidates []string,
) (map[string]*store.TickerState, error) {
	// The massive rest client type is in github.com/massive-com/client-go/v2/rest (we keep shim below)
	cfg := e.cfg

	// worker pool for historical open5m volumes
	type result struct {
		sym        string
		avgPrev10  float64
		todayPct   float64
		err        error
	}

	jobs := make(chan string)
	results := make(chan result)

	workerN := cfg.History.MaxWorkers
	if workerN < 1 {
		workerN = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sym := range jobs {
				t := e.st.GetTicker(sym)
				if t == nil {
					results <- result{sym: sym, err: fmt.Errorf("missing ticker state")}
					continue
				}
				avg, used, err := e.avgPrevSessionsOpen5mVol(ctx, rest, sym, openNY, cfg.History.Open5mLookbackSessions, cfg.History.MaxCalendarLookback)
				if err != nil {
					results <- result{sym: sym, err: err}
					continue
				}
				var pct float64
				if avg > 0 {
					pct = (t.Open5mVol / avg) * 100.0
				}
				_ = used
				results <- result{sym: sym, avgPrev10: avg, todayPct: pct, err: nil}
			}
		}()
	}

	go func() {
		for _, sym := range candidates {
			select {
			case <-ctx.Done():
				break
			case jobs <- sym:
			}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	// Seed VWAP with trades from open->09:35 for each candidate
	// (trade-level VWAP is “better data” than bar typical-price approximation)
	tracked := make(map[string]*store.TickerState, len(candidates))

	// first, create shallow states from store
	for _, sym := range candidates {
		t := e.st.GetTicker(sym)
		if t == nil {
			continue
		}
		t.Status = "tracking"
		tracked[sym] = t
	}

	// apply history results
	for r := range results {
		if r.err != nil {
			e.emit(time.Now().In(e.loc), "SYSTEM", r.sym, fmt.Sprintf("History calc failed: %v", r.err), "", "warn")
			continue
		}
		ts := tracked[r.sym]
		if ts == nil {
			continue
		}
		ts.Prev10AvgOpen5mVol = r.avgPrev10
		ts.Open5mTodayPct = r.todayPct
	}

	// seed VWAP from REST trades (open->09:35) for tracked tickers
	for _, sym := range candidates {
		ts := tracked[sym]
		if ts == nil {
			continue
		}

		if err := e.seedVWAPFromTrades(ctx, rest, sym, openNY, selNY, ts); err != nil {
			e.emit(time.Now().In(e.loc), "SYSTEM", sym, fmt.Sprintf("VWAP seed failed: %v", err), "", "warn")
		}
	}

	// Put updated states back in store
	for sym, ts := range tracked {
		e.st.UpsertTicker(sym, func(t *store.TickerState) {
			*t = *ts
		})
	}

	return tracked, nil
}

func (e *Engine) seedVWAPFromTrades(ctx context.Context, rest *mrestClientShim, sym string, startNY, endNY time.Time, ts *store.TickerState) error {
	it := rest.ListTrades(ctx, sym, startNY, endNY)
	for it.Next() {
		tr := it.Item()
		// models.Trade has Price/Size (float64) and Timestamp (nanos) in the REST models :contentReference[oaicite:8]{index=8}
		if tr.Price <= 0 || tr.Size <= 0 {
			continue
		}
		ts.CumPV += tr.Price * tr.Size
		ts.CumV += tr.Size
	}
	if err := it.Err(); err != nil {
		return err
	}
	if ts.CumV > 0 {
		ts.VWAP = ts.CumPV / ts.CumV
		ts.PrevVWAP = ts.VWAP
	}
	return nil
}

func (e *Engine) avgPrevSessionsOpen5mVol(
	ctx context.Context,
	rest *mrestClientShim,
	sym string,
	openNY time.Time,
	sessionsNeeded int,
	maxLookbackDays int,
) (avg float64, used int, err error) {
	var vols []float64
	day := openNY

	for back := 1; back <= maxLookbackDays && len(vols) < sessionsNeeded; back++ {
		d := day.AddDate(0, 0, -back)
		start := time.Date(d.Year(), d.Month(), d.Day(), 9, 30, 0, 0, e.loc)
		end := start.Add(5 * time.Minute)

		v, ok, err := rest.Open5mVolume(ctx, sym, start, end)
		if err != nil {
			continue
		}
		if !ok || v <= 0 {
			continue
		}
		vols = append(vols, v)
	}

	if len(vols) == 0 {
		return 0, 0, fmt.Errorf("no prior sessions found in lookback=%d days", maxLookbackDays)
	}
	sum := 0.0
	for _, v := range vols {
		sum += v
	}
	return sum / float64(len(vols)), len(vols), nil
}

// ---- Trades processing (tick data) ----
func (e *Engine) onTrade(openNY, selNY, cutoffNY, exitNY time.Time, tr massive.EquityTrade) {
	e.onTradeFields(openNY, selNY, cutoffNY, exitNY, tr.Symbol, tr.Timestamp, tr.Price, float64(tr.Size))
}

func (e *Engine) onTradeFields(openNY, selNY, cutoffNY, exitNY time.Time, sym string, tsMillis int64, price float64, size float64) {
	if sym == "" {
		return
	}

	// tracked only
	ts := e.st.GetTicker(sym)
	if ts == nil {
		return
	}

	trNY := time.UnixMilli(tsMillis).In(e.loc)
	if trNY.Before(openNY) || trNY.After(exitNY.Add(5*time.Minute)) {
		return
	}

	if price <= 0 || size <= 0 {
		return
	}

	minAfterOpen := trNY.Sub(openNY).Seconds() / 60.0

	// update VWAP & last (always, even after exit, so we can compute hold-to-cutoff + MFE/MAE)
	e.st.UpsertTicker(sym, func(t *store.TickerState) {
		t.LastTrade = trNY
		t.MinutesAfterOpen = minAfterOpen

		// prev values for cross detection
		t.PrevPrice = t.LastPrice
		t.PrevVWAP = t.VWAP

		// vwap update (trade-level)
		t.CumPV += price * size
		t.CumV += size
		if t.CumV > 0 {
			t.VWAP = t.CumPV / t.CumV
		}

		t.LastPrice = price

		// track MFE/MAE after entry, up to whatever data we process (historic ends at cutoff)
		if t.HasPosition && !t.EntryTime.IsZero() && !trNY.Before(t.EntryTime) {
			if t.MaxPriceSinceEntry == 0 || price > t.MaxPriceSinceEntry {
				t.MaxPriceSinceEntry = price
				t.MaxPriceSinceEntryTime = trNY
			}
			if t.MinPriceSinceEntry == 0 || price < t.MinPriceSinceEntry {
				t.MinPriceSinceEntry = price
				t.MinPriceSinceEntryTime = trNY
			}
		}
	})

	// Read back for logic checks
	ts = e.st.GetTicker(sym)
	if ts == nil {
		return
	}

	// BUY window checks
	if !ts.HasPosition && !ts.Exited {
		if trNY.Before(selNY) {
			return
		}
		if !trNY.Before(cutoffNY) {
			return
		}

		// entry minutes window: 5..12 inclusive
		if minAfterOpen < float64(e.cfg.Filters.EntryMinAfterOpen) || minAfterOpen > float64(e.cfg.Filters.EntryMaxAfterOpen)+0.999 {
			return
		}

		// detect cross (for diagnostics too)
		cross := ts.PrevPrice > 0 && ts.PrevVWAP > 0 && ts.PrevPrice < ts.PrevVWAP && price >= ts.VWAP
		if cross {
			e.st.UpsertTicker(sym, func(t *store.TickerState) {
				if !t.SawCrossInWindow {
					t.SawCrossInWindow = true
					t.FirstCrossTime = trNY
					t.FirstCrossPrice = price
				}
			})
		}

		// must meet open metrics + today pct + entry price filters
		if ts.Open5mRangePct < e.cfg.Filters.Open5mRangePctMin || ts.Open5mRangePct > e.cfg.Filters.Open5mRangePctMax {
			return
		}
		if ts.Open5mVol < e.cfg.Filters.Open5mVolMin || ts.Open5mVol > e.cfg.Filters.Open5mVolMax {
			return
		}
		if ts.Open5mTodayPct < e.cfg.Filters.Open5mTodayPctMin || ts.Open5mTodayPct > e.cfg.Filters.Open5mTodayPctMax {
			return
		}
		if price < e.cfg.Filters.EntryPriceMin || price > e.cfg.Filters.EntryPriceMax {
			return
		}

		// VWAP cross-up from below:
		if cross {
			e.openPosition(trNY, sym, price)
			return
		}
	}

	// manage position
	if ts.HasPosition && !ts.Exited {
		if price >= ts.TakeProfitPrice && ts.TakeProfitPrice > 0 {
			e.closePosition(trNY, sym, "PROFIT", fmt.Sprintf("PROFIT! %s", sym), price)
			return
		}
		if price <= ts.StopPrice && ts.StopPrice > 0 {
			e.closePosition(trNY, sym, "STOP", fmt.Sprintf("STOP LOSS HIT! %s", sym), price)
			return
		}
	}
}

func (e *Engine) openPosition(tsNY time.Time, sym string, entry float64) {
	tp := entry * (1.0 + e.cfg.Risk.TakeProfitPct)
	sl := entry * (1.0 - e.cfg.Risk.StopLossPct)

	openNY, _, _, _ := e.st.Times()
	minAfterOpen := tsNY.Sub(openNY).Seconds() / 60.0

	e.st.UpsertTicker(sym, func(t *store.TickerState) {
		t.HasPosition = true
		t.EntryPrice = entry
		t.EntryTime = tsNY
		t.EntryMinutesAfterOpen = minAfterOpen
		t.TakeProfitPrice = tp
		t.StopPrice = sl
		t.Status = "LONG"

		// initialize excursion trackers at entry
		t.MaxPriceSinceEntry = entry
		t.MaxPriceSinceEntryTime = tsNY
		t.MinPriceSinceEntry = entry
		t.MinPriceSinceEntryTime = tsNY
	})

	msg := fmt.Sprintf("BUY %s (%s)", sym, nato.SpellNATO(sym))
	audioID := e.say(tsNY, "BUY", sym, "Buy. "+nato.SpellNATO(sym))
	e.emit(tsNY, "BUY", sym, msg, audioID, "signal")
}

func (e *Engine) closePosition(tsNY time.Time, sym, reason, ttsText string, exitPrice float64) {
	openNY, _, _, _ := e.st.Times()
	minAfterOpen := tsNY.Sub(openNY).Seconds() / 60.0

	e.st.UpsertTicker(sym, func(t *store.TickerState) {
		t.Exited = true
		t.ExitReason = reason
		t.ExitTime = tsNY
		t.ExitPrice = exitPrice
		t.ExitMinutesAfterOpen = minAfterOpen
		t.Status = reason
	})

	audioID := e.say(tsNY, reason, sym, ttsText)
	e.emit(tsNY, reason, sym, ttsText, audioID, "signal")
}

func (e *Engine) onElevenAM(tsNY time.Time) {
	audioID := e.say(tsNY, "11AM", "", "Eleven a.m. close")
	e.emit(tsNY, "11AM", "", "11am close", audioID, "info")

	// close any open positions at time exit (use last known price)
	wl := e.st.Watchlist()
	for _, sym := range wl {
		t := e.st.GetTicker(sym)
		if t == nil {
			continue
		}
		if t.HasPosition && !t.Exited {
			px := t.LastPrice
			if px <= 0 {
				px = t.EntryPrice
			}
			e.closePosition(tsNY, sym, "TIME_EXIT", fmt.Sprintf("11am close. %s", sym), px)
		}
	}
}

// ---- Event + TTS helpers ----
func (e *Engine) emit(tsNY time.Time, typ, sym, msg, audioID, level string) {
	ev := store.Event{
		ID:      fmt.Sprintf("%d-%s-%s", tsNY.UnixNano(), typ, sym),
		TimeNY:  tsNY.Format("15:04:05"),
		Type:    typ,
		Symbol:  sym,
		Message: msg,
		AudioID: audioID,
		Level:   level,
	}
	e.st.AddEvent(ev)
}

func (e *Engine) say(tsNY time.Time, typ, sym, text string) string {
	if e.tts == nil || !e.tts.Enabled() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	audioID, audioBytes, _, err := e.tts.Synthesize(ctx, text)
	if err != nil {
		log.Printf("tts failed (%s %s): %v", typ, sym, err)
		return ""
	}
	e.st.StoreAudio(audioID, audioBytes)
	return audioID
}

// ---- Time helpers ----
func atTime(now time.Time, hms string, loc *time.Location) time.Time {
	// hms "HH:MM:SS"
	var hh, mm, ss int
	_, _ = fmt.Sscanf(hms, "%d:%d:%d", &hh, &mm, &ss)
	return time.Date(now.Year(), now.Month(), now.Day(), hh, mm, ss, 0, loc)
}
