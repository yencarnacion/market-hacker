package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/massive-com/client-go/v2/rest/models"
	massivews "github.com/massive-com/client-go/v2/websocket"

	"massive-orb/internal/massive"
	"massive-orb/internal/store"
)

const historicShares = 1000

// correctCandidatesOpen5mViaREST re-fetches the official 09:30–09:35 bars for the selected tickers only,
// then re-applies the current open5m filters. This is used in "start after 09:30" scenarios to avoid
// REST-calling the full watchlist but still make final candidates accurate.
func (e *Engine) correctCandidatesOpen5mViaREST(ctx context.Context, rest *mrestClientShim, openNY, selNY time.Time, candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}

	f := e.st.Filters()

	type res struct {
		sym string
		o   float64
		hi  float64
		lo  float64
		vol float64
		ok  bool
		err error
	}

	jobs := make(chan string)
	results := make(chan res)

	workerN := e.cfg.History.MaxWorkers
	if workerN < 1 {
		workerN = 1
	}
	if workerN > len(candidates) {
		workerN = len(candidates)
	}

	var wg sync.WaitGroup
	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sym := range jobs {
				o, hi, lo, vol, ok, err := rest.Open5mMetrics(ctx, sym, openNY, selNY)
				results <- res{sym: sym, o: o, hi: hi, lo: lo, vol: vol, ok: ok, err: err}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, sym := range candidates {
			select {
			case <-ctx.Done():
				return
			case jobs <- sym:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]string, 0, len(candidates))
	for r := range results {
		if r.err != nil || !r.ok || r.o <= 0 || r.hi <= 0 || r.lo <= 0 {
			continue
		}
		rng := (r.hi - r.lo) / r.o

		e.st.UpsertTicker(r.sym, func(t *store.TickerState) {
			t.Open0930 = r.o
			t.Open0930Estimated = false
			t.ORHigh = r.hi
			t.ORLow = r.lo
			t.Open5mVol = r.vol
			t.Open5mRangePct = rng
		})

		if rng >= f.Open5mRangePctMin && rng <= f.Open5mRangePctMax &&
			r.vol >= f.Open5mVolMin && r.vol <= f.Open5mVolMax {
			out = append(out, r.sym)
		}
	}
	sort.Strings(out)
	return out, nil
}

// runHistoricLiveToday:
// If the requested session is "today" and current time is before the configured 11:00 force exit,
// we replay/catch-up what we can with REST (without emitting past BUY/SELL actions),
// then switch to live websockets and run until 11:00.
func (e *Engine) runHistoricLiveToday(ctx context.Context, rest *mrestClientShim, sessionDayNY time.Time, openNY, selNY, cutoffNY, exitNY time.Time) error {
	// Wait for open if needed
	nowNY := time.Now().In(e.loc)
	if nowNY.Before(openNY) {
		e.st.SetPhase(store.PhaseWaitingOpen)
		e.emit(nowNY, "SYSTEM", "", fmt.Sprintf("HISTORIC-LIVE: waiting for open at %s", openNY.Format("15:04:05")), "", "info")
		timer := time.NewTimer(time.Until(openNY))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}

	// Phase 1: collect open-5m metrics
	nowNY = time.Now().In(e.loc)
	if nowNY.Before(selNY) {
		e.st.SetPhase(store.PhaseCollecting5m)
		e.emit(nowNY, "SYSTEM", "", "HISTORIC-LIVE: collecting minute bars via WebSocket until 09:35…", "", "info")

		wsAgg, err := massive.NewWS(e.massiveKey, e.cfg.Massive.Feed)
		if err != nil {
			return err
		}
		defer wsAgg.Close()

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

		timer := time.NewTimer(time.Until(selNY))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		wsAgg.Close()
		<-done0935
	} else {
		e.st.SetPhase(store.PhaseCollecting5m)
		e.emit(nowNY, "SYSTEM", "", "HISTORIC-LIVE: started after 09:35 — fetching 09:30–09:34 minute bars via REST…", "", "info")
		if err := e.collectOpen5mViaREST(ctx, rest, openNY, selNY); err != nil {
			return err
		}
	}

	// Candidate selection
	e.st.SetPhase(store.PhaseSelecting0935)
	candidates := e.selectCandidatesAt0935()
	if len(candidates) == 0 {
		e.emit(time.Now().In(e.loc), "SYSTEM", "", "No tickers matched open_5m filters at 09:35.", "", "info")
		e.st.SetPhase(store.PhaseClosed)
		rep := e.buildHistoricReport(sessionDayNY, openNY, selNY, cutoffNY, exitNY, time.Now().In(e.loc))
		e.st.SetHistoricReport(&rep)
		return nil
	}

	// If we started after 09:30 and Open0930 may be estimated, correct candidates only (cheap).
	corrected, _ := e.correctCandidatesOpen5mViaREST(ctx, rest, openNY, selNY, candidates)
	if len(corrected) == 0 {
		e.emit(time.Now().In(e.loc), "SYSTEM", "", "After REST correction, no tickers matched open_5m filters.", "", "info")
		e.st.SetPhase(store.PhaseClosed)
		rep := e.buildHistoricReport(sessionDayNY, openNY, selNY, cutoffNY, exitNY, time.Now().In(e.loc))
		e.st.SetHistoricReport(&rep)
		return nil
	}
	candidates = corrected

	e.emit(time.Now().In(e.loc), "SYSTEM", "", fmt.Sprintf("09:35 selection: %d tickers matched (live tracking to 11:00).", len(candidates)), "", "info")

	tracked, err := e.buildTrackedStates(ctx, rest, openNY, selNY, candidates)
	if err != nil {
		return err
	}
	e.st.SetTrackedTickers(tracked)
	e.st.SetPhase(store.PhaseTrackingTicks)

	// Catch up trades from 09:35 -> now, but DO NOT open/close positions retroactively.
	nowNY = time.Now().In(e.loc)
	syms := make([]string, 0, len(tracked))
	for sym := range tracked {
		syms = append(syms, sym)
	}
	sort.Strings(syms)
	if nowNY.After(selNY) {
		e.emit(nowNY, "SYSTEM", "", fmt.Sprintf("Catch-up (no actions): replaying trades via REST (%s → %s) for %d tickers…",
			selNY.Format("15:04:05"), nowNY.Format("15:04:05"), len(syms)), "", "info")
		for _, sym := range syms {
			it := rest.ListTrades(ctx, sym, selNY, nowNY)
			for it.Next() {
				select {
				case <-ctx.Done():
					return nil
				default:
				}
				tr := it.Item()
				tsMillis := tradeTimeMillis(tr)
				if tsMillis == 0 {
					continue
				}
				e.processTrade(openNY, selNY, cutoffNY, exitNY, sym, tsMillis, tr.Price, tr.Size, false)
			}
			_ = it.Err()
		}
	}

	// Live trades via WebSocket until 11:00
	wsTrades, err := massive.NewWS(e.massiveKey, e.cfg.Massive.Feed)
	if err != nil {
		return err
	}
	defer wsTrades.Close()

	if err := wsTrades.Subscribe(massivews.StocksTrades, syms...); err != nil {
		return fmt.Errorf("subscribe trades: %w", err)
	}
	if err := wsTrades.Connect(); err != nil {
		return fmt.Errorf("ws trades connect: %w", err)
	}

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

	e.emit(time.Now().In(e.loc), "SYSTEM", "", "HISTORIC-LIVE: tracking live trades (VWAP cross logic active)…", "", "info")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-closed11am:
			e.st.SetPhase(store.PhaseClosed)
			rep := e.buildHistoricReport(sessionDayNY, openNY, selNY, cutoffNY, exitNY, exitNY)
			e.st.SetHistoricReport(&rep)
			e.emit(time.Now().In(e.loc), "SYSTEM", "", "Historic report ready (see the web UI).", "", "info")
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

// RunHistoricForDate replays the ORB session for the requested date (NY).
// If the market is closed / no data exists (weekends, holidays), it falls back to the most recent prior session.
func (e *Engine) RunHistoricForDate(ctx context.Context, targetDateNY time.Time) error {
	asOfNY := time.Now().In(e.loc)
	targetDayNY := dateOnlyInLoc(targetDateNY, e.loc)

	restClient := massive.NewREST(e.massiveKey)
	rest := newRESTShim(restClient)

	resolvedDayNY, note, err := e.resolveHistoricSessionDate(ctx, rest, targetDayNY, asOfNY)
	if err != nil {
		e.st.SetPhase(store.PhaseClosed)
		e.emit(asOfNY, "SYSTEM", "", fmt.Sprintf("Historic date resolution failed for %s: %v", targetDayNY.Format("2006-01-02"), err), "", "warn")
		return err
	}

	// Reset store for a clean replay + UI session boundary
	e.st.ResetForHistoricRun(targetDayNY, resolvedDayNY, note)

	openNY := atTime(resolvedDayNY, e.cfg.Market.OpenTime, e.loc)
	selNY := atTime(resolvedDayNY, e.cfg.Market.SelectionTime, e.loc)
	cutoffNY := atTime(resolvedDayNY, e.cfg.Market.VWAPCrossCutoff, e.loc)
	exitNY := atTime(resolvedDayNY, e.cfg.Market.ForceExitTime, e.loc)

	// NEW: If "today" and before 11:00, switch to hybrid live mode:
	// - collect open5m (WS if before 09:35, otherwise REST)
	// - catch-up trades to now (no retro actions)
	// - run live to 11:00
	if sameDayInLoc(resolvedDayNY, asOfNY, e.loc) && asOfNY.Before(exitNY) {
		e.st.SetTimes(openNY, selNY, cutoffNY, exitNY)
		e.st.SetPhase(store.PhaseCollecting5m)
		e.emit(asOfNY, "SYSTEM", "", fmt.Sprintf("HISTORIC-LIVE mode: %s (live until %s).", resolvedDayNY.Format("2006-01-02"), exitNY.Format("15:04:05")), "", "info")
		if note != "" {
			e.emit(asOfNY, "SYSTEM", "", note, "", "info")
		}
		e.emit(asOfNY, "SYSTEM", "", "Audio alerts disabled in historic mode.", "", "info")
		return e.runHistoricLiveToday(ctx, rest, resolvedDayNY, openNY, selNY, cutoffNY, exitNY)
	}

	// IMPORTANT: avoid lookahead only when replaying "today".
	endNY := exitNY
	if sameDayInLoc(resolvedDayNY, asOfNY, e.loc) && asOfNY.Before(exitNY) {
		endNY = asOfNY
	}

	e.st.SetTimes(openNY, selNY, cutoffNY, exitNY)
	e.st.SetPhase(store.PhaseCollecting5m)

	e.emit(asOfNY, "SYSTEM", "", fmt.Sprintf("HISTORIC mode: replaying %s from %s → %s (force exit %s).",
		resolvedDayNY.Format("2006-01-02"),
		openNY.Format("15:04:05"),
		endNY.Format("15:04:05"),
		exitNY.Format("15:04:05"),
	), "", "info")
	if note != "" {
		e.emit(asOfNY, "SYSTEM", "", note, "", "info")
	}
	e.emit(asOfNY, "SYSTEM", "", "Audio alerts disabled in historic mode.", "", "info")

	// Phase 1: build open-5m metrics via REST aggs
	e.emit(asOfNY, "SYSTEM", "", "Fetching 09:30–09:34 minute bars via REST for open-5m metrics...", "", "info")
	if err := e.collectOpen5mViaREST(ctx, rest, openNY, selNY); err != nil {
		e.st.SetPhase(store.PhaseClosed)
		return err
	}

	e.st.SetPhase(store.PhaseSelecting0935)
	candidates := e.selectCandidatesAt0935()
	if len(candidates) == 0 {
		e.emit(time.Now().In(e.loc), "SYSTEM", "", "No tickers matched open_5m filters at 09:35.", "", "info")
		e.st.SetPhase(store.PhaseClosed)

		rep := e.buildHistoricReport(resolvedDayNY, openNY, selNY, cutoffNY, exitNY, endNY)
		e.st.SetHistoricReport(&rep)
		return nil
	}

	e.emit(time.Now().In(e.loc), "SYSTEM", "", fmt.Sprintf("09:35 selection: %d tickers matched opening filters (REST replay continues)", len(candidates)), "", "info")

	tracked, err := e.buildTrackedStates(ctx, rest, openNY, selNY, candidates)
	if err != nil {
		return err
	}
	e.st.SetTrackedTickers(tracked)
	e.st.SetPhase(store.PhaseTrackingTicks)

	// Phase 2: replay trades per ticker from 09:35 → endNY
	syms := make([]string, 0, len(tracked))
	for sym := range tracked {
		syms = append(syms, sym)
	}
	sort.Strings(syms)

	e.emit(time.Now().In(e.loc), "SYSTEM", "", fmt.Sprintf("Replaying trades via REST (%s → %s) for %d tickers...",
		selNY.Format("15:04:05"),
		endNY.Format("15:04:05"),
		len(syms),
	), "", "info")

	for _, sym := range syms {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		it := rest.ListTrades(ctx, sym, selNY, endNY)
		for it.Next() {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			tr := it.Item()
			tsMillis := tradeTimeMillis(tr)
			if tsMillis == 0 {
				continue
			}
			e.processTrade(openNY, selNY, cutoffNY, endNY, sym, tsMillis, tr.Price, tr.Size, true)
		}
		if err := it.Err(); err != nil {
			e.emit(time.Now().In(e.loc), "SYSTEM", sym, fmt.Sprintf("trade replay failed: %v", err), "", "warn")
		}
	}

	// Force-exit close if we actually reached the configured exit window.
	if !endNY.Before(exitNY) {
		e.onElevenAM(exitNY)
	} else {
		// Close any open positions at endNY without pretending it was 11:00.
		e.emit(endNY, "SYSTEM", "", fmt.Sprintf("Historic run ended early at %s; closing any open positions at last price.", endNY.Format("15:04:05")), "", "warn")
		e.closeAllOpenPositionsAt(endNY)
	}

	e.st.SetPhase(store.PhaseClosed)

	rep := e.buildHistoricReport(resolvedDayNY, openNY, selNY, cutoffNY, exitNY, endNY)
	e.st.SetHistoricReport(&rep)

	e.emit(time.Now().In(e.loc), "SYSTEM", "", "Historic report ready (see the web UI).", "", "info")
	return nil
}

func dateOnlyInLoc(t time.Time, loc *time.Location) time.Time {
	tt := t.In(loc)
	return time.Date(tt.Year(), tt.Month(), tt.Day(), 0, 0, 0, 0, loc)
}

func sameDayInLoc(a, b time.Time, loc *time.Location) bool {
	aa := a.In(loc)
	bb := b.In(loc)
	return aa.Year() == bb.Year() && aa.Month() == bb.Month() && aa.Day() == bb.Day()
}

// resolveHistoricSessionDate returns a trading session date to use:
// - clamps future dates
// - skips weekends
// - detects holidays/closed days by probing for open-5m data; falls back to prior session
func (e *Engine) resolveHistoricSessionDate(ctx context.Context, rest *mrestClientShim, targetDayNY, asOfNY time.Time) (resolvedDayNY time.Time, note string, err error) {
	targetDayNY = dateOnlyInLoc(targetDayNY, e.loc)
	todayNY := dateOnlyInLoc(asOfNY, e.loc)
	if targetDayNY.After(todayNY) {
		return time.Time{}, "", fmt.Errorf("date is in the future (max %s)", todayNY.Format("2006-01-02"))
	}

	d := targetDayNY
	// start by skipping weekends fast
	if d.Weekday() == time.Saturday {
		d = d.AddDate(0, 0, -1)
	} else if d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -2)
	}

	maxAttempts := e.cfg.History.MaxCalendarLookback + 15
	if maxAttempts < 10 {
		maxAttempts = 10
	}

	for i := 0; i < maxAttempts; i++ {
		select {
		case <-ctx.Done():
			return time.Time{}, "", ctx.Err()
		default:
		}

		openNY := atTime(d, e.cfg.Market.OpenTime, e.loc)
		selNY := atTime(d, e.cfg.Market.SelectionTime, e.loc)

		ok, derr := e.hasOpen5mData(ctx, rest, openNY, selNY)
		if derr != nil {
			return time.Time{}, "", derr
		}
		if ok {
			if !sameDayInLoc(d, targetDayNY, e.loc) {
				return d, fmt.Sprintf("Requested %s; market closed / no data — showing %s.", targetDayNY.Format("2006-01-02"), d.Format("2006-01-02")), nil
			}
			return d, "", nil
		}

		// step back and skip weekends
		d = d.AddDate(0, 0, -1)
		for d.Weekday() == time.Saturday {
			d = d.AddDate(0, 0, -1)
		}
		for d.Weekday() == time.Sunday {
			d = d.AddDate(0, 0, -2)
		}
	}

	return time.Time{}, "", fmt.Errorf("no market session data found within %d days of %s", maxAttempts, targetDayNY.Format("2006-01-02"))
}

func (e *Engine) hasOpen5mData(ctx context.Context, rest *mrestClientShim, openNY, selNY time.Time) (bool, error) {
	wl := e.st.Watchlist()
	if len(wl) == 0 {
		return false, fmt.Errorf("watchlist is empty")
	}
	n := len(wl)
	if n > 8 {
		n = 8
	}

	var lastErr error
	sawNonErr := false
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		default:
		}
		o, hi, lo, vol, ok, err := rest.Open5mMetrics(ctx, wl[i], openNY, selNY)
		if err != nil {
			lastErr = err
			continue
		}
		sawNonErr = true
		if ok && o > 0 && hi > 0 && lo > 0 && vol > 0 {
			return true, nil
		}
	}
	if !sawNonErr && lastErr != nil {
		return false, lastErr
	}
	return false, nil
}

func (e *Engine) collectOpen5mViaREST(ctx context.Context, rest *mrestClientShim, openNY, selNY time.Time) error {
	wl := e.st.Watchlist()

	type res struct {
		sym  string
		o930 float64
		hi   float64
		lo   float64
		vol  float64
		ok   bool
		err  error
	}

	jobs := make(chan string)
	results := make(chan res)

	workerN := e.cfg.History.MaxWorkers
	if workerN < 1 {
		workerN = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workerN; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sym := range jobs {
				o, hi, lo, vol, ok, err := rest.Open5mMetrics(ctx, sym, openNY, selNY)
				results <- res{sym: sym, o930: o, hi: hi, lo: lo, vol: vol, ok: ok, err: err}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, sym := range wl {
			select {
			case <-ctx.Done():
				return
			case jobs <- sym:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.err != nil {
			continue
		}
		if !r.ok || r.o930 <= 0 || r.hi <= 0 || r.lo <= 0 {
			continue
		}
		e.st.UpsertTicker(r.sym, func(t *store.TickerState) {
			t.Open0930 = r.o930
			t.ORHigh = r.hi
			t.ORLow = r.lo
			t.Open5mVol = r.vol
		})
	}

	return nil
}

func (e *Engine) closeAllOpenPositionsAt(tsNY time.Time) {
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
			e.closePosition(tsNY, sym, "TIME_EXIT", fmt.Sprintf("Close at %s. %s", tsNY.Format("15:04"), sym), px)
		}
	}
}

// tradeTimeMillis picks the best available trade timestamp from the REST model and returns Unix ms.
func tradeTimeMillis(tr models.Trade) int64 {
	// Prefer SIP timestamp (best “tape time”), then participant, then TRF.
	if !time.Time(tr.SipTimestamp).IsZero() {
		return time.Time(tr.SipTimestamp).UnixMilli()
	}
	if !time.Time(tr.ParticipantTimestamp).IsZero() {
		return time.Time(tr.ParticipantTimestamp).UnixMilli()
	}
	if !time.Time(tr.TrfTimestamp).IsZero() {
		return time.Time(tr.TrfTimestamp).UnixMilli()
	}
	return 0
}

func (e *Engine) buildHistoricReport(sessionDateNY, openNY, selNY, cutoffNY, exitNY, endNY time.Time) store.HistoricReport {
	f := e.st.Filters()

	states := e.st.TickerStates()

	trades := make([]store.HistoricTrade, 0, 32)
	noEntries := make([]store.HistoricNoEntry, 0, 64)

	sumPnL := 0.0
	sumNotional := 0.0
	sumPct := 0.0

	sumWinAmt := 0.0
	sumLossAmt := 0.0
	sumWinPct := 0.0
	sumLossPct := 0.0
	winN := 0
	lossN := 0
	timeExitN := 0

	bestPct := 0.0
	worstPct := 0.0
	firstTrade := true

	for _, t := range states {
		if t.HasPosition && t.EntryPrice > 0 && !t.EntryTime.IsZero() {
			entry := t.EntryPrice

			exitPx := t.ExitPrice
			exitTime := t.ExitTime
			if exitPx <= 0 {
				// if still open, approximate with last known
				exitPx = t.LastPrice
				exitTime = endNY
			}

			realPct := (exitPx - entry) / entry
			realAmt := (exitPx - entry) * float64(historicShares)

			holdPx := t.LastPrice
			holdPct := (holdPx - entry) / entry
			holdAmt := (holdPx - entry) * float64(historicShares)

			mfePx := t.MaxPriceSinceEntry
			mfeTime := t.MaxPriceSinceEntryTime
			if mfePx <= 0 {
				mfePx = entry
				mfeTime = t.EntryTime
			}
			mfePct := (mfePx - entry) / entry
			mfeAmt := (mfePx - entry) * float64(historicShares)

			maePx := t.MinPriceSinceEntry
			maeTime := t.MinPriceSinceEntryTime
			if maePx <= 0 {
				maePx = entry
				maeTime = t.EntryTime
			}
			maePct := (maePx - entry) / entry
			maeAmt := (maePx - entry) * float64(historicShares)

			entryTimeNY := t.EntryTime.In(e.loc).Format("15:04:05")
			exitTimeNY := exitTime.In(e.loc).Format("15:04:05")

			trades = append(trades, store.HistoricTrade{
				Symbol:                t.Symbol,
				EntryTimeNY:           entryTimeNY,
				EntryPrice:            entry,
				EntryMinutesAfterOpen: t.EntryMinutesAfterOpen,
				TakeProfitPrice:       t.TakeProfitPrice,
				StopPrice:             t.StopPrice,
				ExitTimeNY:            exitTimeNY,
				ExitPrice:             exitPx,
				ExitMinutesAfterOpen:  t.ExitMinutesAfterOpen,
				ExitReason:            t.ExitReason,
				Shares:                historicShares,
				RealizedPnLPct:        realPct,
				RealizedPnL:           realAmt,
				HoldPrice:             holdPx,
				HoldPnLPct:            holdPct,
				HoldPnL:               holdAmt,
				MFEPrice:              mfePx,
				MFETimeNY:             mfeTime.In(e.loc).Format("15:04:05"),
				MFEPnLPct:             mfePct,
				MFEPnL:                mfeAmt,
				MAEPrice:              maePx,
				MAETimeNY:             maeTime.In(e.loc).Format("15:04:05"),
				MAEPnLPct:             maePct,
				MAEPnL:                maeAmt,
			})

			sumPnL += realAmt
			sumNotional += entry * float64(historicShares)
			sumPct += realPct

			if t.ExitReason == "TIME_EXIT" {
				timeExitN++
			}

			if realAmt >= 0 {
				sumWinAmt += realAmt
				sumWinPct += realPct
				winN++
			} else {
				sumLossAmt += realAmt // negative
				sumLossPct += realPct
				lossN++
			}

			if firstTrade {
				bestPct = realPct
				worstPct = realPct
				firstTrade = false
			} else {
				if realPct > bestPct {
					bestPct = realPct
				}
				if realPct < worstPct {
					worstPct = realPct
				}
			}
		} else {
			// Selected but no entry
			reason := ""
			if !t.SawCrossInWindow {
				start := openNY.Add(1 * time.Minute).Format("15:04")
				end := cutoffNY.Format("15:04")
				reason = fmt.Sprintf("No VWAP cross between %s and %s", start, end)
			} else if t.Open5mTodayPct < f.Open5mTodayPctMin || t.Open5mTodayPct > f.Open5mTodayPctMax {
				reason = "Open5mToday% filter failed"
			} else if t.FirstCrossPrice > 0 && (t.FirstCrossPrice < f.EntryPriceMin || t.FirstCrossPrice > f.EntryPriceMax) {
				reason = "VWAP cross occurred but price filter failed"
			} else {
				reason = "No entry (filters + cross never aligned)"
			}

			noEntries = append(noEntries, store.HistoricNoEntry{
				Symbol:           t.Symbol,
				Open5mVol:        t.Open5mVol,
				Open5mRangePct:   t.Open5mRangePct,
				Open5mTodayPct:   t.Open5mTodayPct,
				SawCrossInWindow: t.SawCrossInWindow,
				FirstCrossTimeNY: func() string {
					if t.FirstCrossTime.IsZero() {
						return ""
					}
					return t.FirstCrossTime.In(e.loc).Format("15:04:05")
				}(),
				FirstCrossPrice: t.FirstCrossPrice,
				Reason:          reason,
			})
		}
	}

	// Sort trades by realized pnl desc for scanability
	sort.Slice(trades, func(i, j int) bool {
		return trades[i].RealizedPnLPct > trades[j].RealizedPnLPct
	})
	sort.Slice(noEntries, func(i, j int) bool {
		return noEntries[i].Symbol < noEntries[j].Symbol
	})

	tradeN := len(trades)
	winRate := 0.0
	if tradeN > 0 {
		winRate = float64(winN) / float64(tradeN)
	}

	netRet := 0.0
	if sumNotional > 0 {
		netRet = sumPnL / sumNotional
	}

	avgRet := 0.0
	if tradeN > 0 {
		avgRet = sumPct / float64(tradeN)
	}

	avgWin := 0.0
	if winN > 0 {
		avgWin = sumWinPct / float64(winN)
	}
	avgLoss := 0.0
	if lossN > 0 {
		avgLoss = sumLossPct / float64(lossN)
	}

	profitFactor := 0.0
	if sumLossAmt < 0 {
		profitFactor = sumWinAmt / (-sumLossAmt)
	} else if sumWinAmt > 0 && sumLossAmt == 0 {
		profitFactor = 999.0
	}

	summary := store.HistoricSummary{
		DateNY:        sessionDateNY.Format("2006-01-02"),
		WindowStartNY: openNY.Format("15:04:05"),
		WindowEndNY:   endNY.Format("15:04:05"),
		Shares:        historicShares,
		Candidates:    len(states),
		TradesTaken:   tradeN,
		NoEntry:       len(noEntries),
		Wins:          winN,
		Losses:        lossN,
		TimeExits:     timeExitN,
		WinRate:       winRate,
		NetPnL:        sumPnL,
		TotalNotional: sumNotional,
		NetReturnPct:  netRet,
		AvgReturnPct:  avgRet,
		AvgWinPct:     avgWin,
		AvgLossPct:    avgLoss,
		ProfitFactor:  profitFactor,
		BestTradePct:  bestPct,
		WorstTradePct: worstPct,
	}

	return store.HistoricReport{
		Summary:   summary,
		Trades:    trades,
		NoEntries: noEntries,
	}
}
