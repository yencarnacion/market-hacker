package massive

import (
	"errors"
	"strings"
	"time"

	mrest "github.com/massive-com/client-go/v2/rest"
	"github.com/massive-com/client-go/v2/rest/models"
	massivews "github.com/massive-com/client-go/v2/websocket"
	wsmodels "github.com/massive-com/client-go/v2/websocket/models"
)

type Clients struct {
	REST *mrest.Client
}

func NewREST(apiKey string) *mrest.Client {
	return mrest.New(apiKey)
}

func NewWS(apiKey, feed string) (*massivews.Client, error) {
	cfg := massivews.Config{
		APIKey: apiKey,
		Market: massivews.Stocks,
	}

	switch strings.ToLower(feed) {
	case "realtime", "real_time", "real-time":
		cfg.Feed = massivews.RealTime
	case "delayed":
		cfg.Feed = massivews.Delayed
	default:
		return nil, errors.New("unknown massive.feed (use realtime or delayed)")
	}

	return massivews.New(cfg)
}

// ---- Helpers to convert time -> Massive param types ----

func ToMillis(t time.Time) models.Millis {
	return models.Millis(t.UTC())
}

func ToNanos(t time.Time) models.Nanos {
	return models.Nanos(t.UTC())
}

// ---- Types re-export (useful in engine without repeating imports) ----

type EquityAgg = wsmodels.EquityAgg
type EquityTrade = wsmodels.EquityTrade
