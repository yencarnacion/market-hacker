package engine

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/massive-com/client-go/v2/rest/models"

	"massive-orb/internal/massive"
	"massive-orb/internal/store"
)

const historicShares = 1000

func (e *Engine) RunHistoric(ctx context.Context) error {
	nowNY := time.Now().In(e.loc)

	openNY := atTime(nowNY, e.cfg.Market.OpenTime, e.loc)
	selNY := atTime(nowNY, e.cfg.Market.SelectionTime, e.loc)
	cutoffNY := atTime(nowNY, e.cfg.Market.VWAPCrossCutoff, e.loc)
	exitNY := atTime(nowNY, e.cfg.Market.ForceExitTime, e.loc)

	// IMPORTANT: avoid lookahead if someone runs historic before force-exit.
	endNY := exitNY
	if nowNY.Before(exitNY) {
		endNY = nowNY
	}

	e.st.SetTimes(openNY, selNY, cutoffNY, exitNY)
	e.st.SetPhase(store.PhaseCollecting5m)

	e.emit(nowNY, "SYSTEM", "", fmt.Sprintf("HISTORIC mode: replaying %s from %s → %s (force exit %s).",
		nowNY.Format("2006-01-02"),
		openNY.Format("15:04:05"),
		endNY.Format("15:04:05"),
		exitNY.Format("15:04:05"),
	), "", "info")
	e.emit(nowNY, "SYSTEM", "", "Audio alerts disabled in historic mode.", "", "info")

	restClient := massive.NewREST(e.massiveKey)
	rest := newRESTShim(restClient)

	// Phase 1: build open-5m metrics via REST aggs
	e.emit(nowNY, "SYSTEM", "", "Fetching 09:30–09:34 minute bars via REST for open-5m metrics...", "", "info")
	if err := e.collectOpen5mViaREST(ctx, rest, openNY, selNY); err != nil {
		return err
	}

	e.st.SetPhase(store.PhaseSelecting0935)
	candidates := e.selectCandidatesAt0935()
	if len(candidates) == 0 {
		e.emit(time.Now().In(e.loc), "SYSTEM", "", "No tickers matched open_5m filters at 09:35.", "", "info")
		e.st.SetPhase(store.PhaseClosed)

		rep := e.buildHistoricReport(nowNY, openNY, selNY, cutoffNY, exitNY, endNY)
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
		it := rest.ListTrades(ctx, sym, selNY, endNY)
		for it.Next() {
			tr := it.Item()
			tsMillis := tradeTimeMillis(tr)
			if tsMillis == 0 {
				continue
			}
			e.onTradeFields(openNY, selNY, cutoffNY, endNY, sym, tsMillis, tr.Price, tr.Size)
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

	rep := e.buildHistoricReport(nowNY, openNY, selNY, cutoffNY, exitNY, endNY)
	e.st.SetHistoricReport(&rep)

	e.emit(time.Now().In(e.loc), "SYSTEM", "", "Historic report ready (see the web UI).", "", "info")
	return nil
}

func (e *Engine) collectOpen5mViaREST(ctx context.Context, rest *mrestClientShim, openNY, selNY time.Time) error {
	wl := e.st.Watchlist()

	type res struct {
		sym   string
		o930  float64
		hi    float64
		lo    float64
		vol   float64
		ok    bool
		err   error
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

func (e *Engine) buildHistoricReport(nowNY, openNY, selNY, cutoffNY, exitNY, endNY time.Time) store.HistoricReport {
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
				reason = "No VWAP cross in entry window"
			} else if t.Open5mTodayPct < e.cfg.Filters.Open5mTodayPctMin || t.Open5mTodayPct > e.cfg.Filters.Open5mTodayPctMax {
				reason = "Open5mToday% filter failed"
			} else if t.FirstCrossPrice > 0 && (t.FirstCrossPrice < e.cfg.Filters.EntryPriceMin || t.FirstCrossPrice > e.cfg.Filters.EntryPriceMax) {
				reason = "VWAP cross occurred but price filter failed"
			} else {
				reason = "No entry (filters + cross never aligned)"
			}

			noEntries = append(noEntries, store.HistoricNoEntry{
				Symbol:            t.Symbol,
				Open5mVol:         t.Open5mVol,
				Open5mRangePct:    t.Open5mRangePct,
				Open5mTodayPct:    t.Open5mTodayPct,
				SawCrossInWindow:  t.SawCrossInWindow,
				FirstCrossTimeNY:  func() string { if t.FirstCrossTime.IsZero() { return "" }; return t.FirstCrossTime.In(e.loc).Format("15:04:05") }(),
				FirstCrossPrice:   t.FirstCrossPrice,
				Reason:            reason,
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
		DateNY:        nowNY.Format("2006-01-02"),
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
