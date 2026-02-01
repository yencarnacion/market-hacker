package engine

import (
	"context"
	"time"

	mrest "github.com/massive-com/client-go/v2/rest"
	"github.com/massive-com/client-go/v2/rest/iter"
	"github.com/massive-com/client-go/v2/rest/models"

	"massive-orb/internal/massive"
)

type mrestClientShim struct {
	c *mrest.Client
}

func newRESTShim(c *mrest.Client) *mrestClientShim {
	return &mrestClientShim{c: c}
}

// Open5mVolume sums 1-minute aggregate volumes in [start,end) (expected 09:30-09:35).
func (r *mrestClientShim) Open5mVolume(ctx context.Context, ticker string, startNY, endNY time.Time) (vol float64, ok bool, err error) {
	params := models.ListAggsParams{
		Ticker:     ticker,
		Multiplier: 1,
		Timespan:   models.Minute,
		From:       massive.ToMillis(startNY),
		To:         massive.ToMillis(endNY),
	}
	it := r.c.ListAggs(ctx, &params)
	sum := 0.0
	n := 0
	for it.Next() {
		a := it.Item()
		// cast defensively in case Volume isn't float64 in the SDK model
		v := float64(a.Volume)
		if v > 0 {
			sum += v
			n++
		}
	}
	if err := it.Err(); err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, nil
	}
	return sum, true, nil
}

// Open5mMetrics returns (open0930, orHigh, orLow, vol) for 1-minute bars in [start,end).
func (r *mrestClientShim) Open5mMetrics(ctx context.Context, ticker string, startNY, endNY time.Time) (open0930, orHigh, orLow, vol float64, ok bool, err error) {
	params := models.ListAggsParams{
		Ticker:     ticker,
		Multiplier: 1,
		Timespan:   models.Minute,
		From:       massive.ToMillis(startNY),
		To:         massive.ToMillis(endNY),
	}
	it := r.c.ListAggs(ctx, &params)

	n := 0
	for it.Next() {
		a := it.Item()
		if a.Open <= 0 {
			continue
		}
		if n == 0 {
			open0930 = a.Open
			orHigh = a.High
			orLow = a.Low
		} else {
			if a.High > orHigh {
				orHigh = a.High
			}
			if orLow == 0 || a.Low < orLow {
				orLow = a.Low
			}
		}
		vol += a.Volume
		n++
	}
	if err := it.Err(); err != nil {
		return 0, 0, 0, 0, false, err
	}
	if n == 0 {
		return 0, 0, 0, 0, false, nil
	}
	return open0930, orHigh, orLow, vol, true, nil
}

type TradeIter struct {
	it *iter.Iter[models.Trade]
}

func (t TradeIter) Next() bool         { return t.it.Next() }
func (t TradeIter) Item() models.Trade { return t.it.Item() }
func (t TradeIter) Err() error         { return t.it.Err() }

func (r *mrestClientShim) ListTrades(ctx context.Context, ticker string, startNY, endNY time.Time) TradeIter {
	params := models.ListTradesParams{Ticker: ticker}.
		WithTimestamp(models.GTE, massive.ToNanos(startNY)).
		WithTimestamp(models.LT, massive.ToNanos(endNY)).
		WithLimit(50000)

	it := r.c.ListTrades(ctx, params)
	return TradeIter{it: it}
}
