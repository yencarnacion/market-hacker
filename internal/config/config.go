package config

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"server"`

	Market struct {
		Timezone        string `yaml:"timezone"`
		OpenTime        string `yaml:"open_time"`
		SelectionTime   string `yaml:"selection_time"`
		VWAPCrossCutoff string `yaml:"vwap_cross_cutoff_time"`
		ForceExitTime   string `yaml:"force_exit_time"`
	} `yaml:"market"`

	Filters struct {
		Open5mRangePctMin float64 `yaml:"open_5m_range_pct_min"`
		Open5mRangePctMax float64 `yaml:"open_5m_range_pct_max"`
		Open5mVolMin      float64 `yaml:"open_5m_vol_min"`
		Open5mVolMax      float64 `yaml:"open_5m_vol_max"`

		Open5mTodayPctMin float64 `yaml:"open_5m_today_pct_min"`
		Open5mTodayPctMax float64 `yaml:"open_5m_today_pct_max"`

		EntryMinAfterOpen int     `yaml:"entry_minutes_after_open_min"`
		EntryMaxAfterOpen int     `yaml:"entry_minutes_after_open_max"`
		EntryPriceMin     float64 `yaml:"entry_price_min"`
		EntryPriceMax     float64 `yaml:"entry_price_max"`
	} `yaml:"filters"`

	Risk struct {
		TakeProfitPct float64 `yaml:"take_profit_pct"`
		StopLossPct   float64 `yaml:"stop_loss_pct"`
	} `yaml:"risk"`

	History struct {
		Open5mLookbackSessions int `yaml:"open5m_lookback_sessions"`
		MaxCalendarLookback    int `yaml:"max_calendar_lookback_days"`
		MaxWorkers             int `yaml:"max_workers"`
	} `yaml:"history"`

	Massive struct {
		Feed        string `yaml:"feed"` // realtime | delayed
		Market      string `yaml:"market"`
		WSBatchSize int    `yaml:"ws_batch_size"`
	} `yaml:"massive"`

	OpenAI struct {
		TTSModel       string `yaml:"tts_model"`
		Voice          string `yaml:"voice"`
		ResponseFormat string `yaml:"response_format"`
	} `yaml:"openai"`

	UI struct {
		MaxEvents int `yaml:"max_events"`
	} `yaml:"ui"`
}

func Load(path string) (Config, error) {
	var cfg Config

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}

	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8097
	}
	if cfg.Market.Timezone == "" {
		cfg.Market.Timezone = "America/New_York"
	}
	if cfg.Market.OpenTime == "" {
		cfg.Market.OpenTime = "09:30:00"
	}
	if cfg.Market.SelectionTime == "" {
		cfg.Market.SelectionTime = "09:35:00"
	}
	if cfg.Market.VWAPCrossCutoff == "" {
		cfg.Market.VWAPCrossCutoff = "09:43:00"
	}
	if cfg.Market.ForceExitTime == "" {
		cfg.Market.ForceExitTime = "11:00:00"
	}

	if cfg.Massive.Feed == "" {
		cfg.Massive.Feed = "realtime"
	}
	if cfg.Massive.Market == "" {
		cfg.Massive.Market = "stocks"
	}
	if cfg.Massive.WSBatchSize <= 0 {
		cfg.Massive.WSBatchSize = 200
	}

	if cfg.History.Open5mLookbackSessions <= 0 {
		cfg.History.Open5mLookbackSessions = 10
	}
	if cfg.History.MaxCalendarLookback <= 0 {
		cfg.History.MaxCalendarLookback = 35
	}
	if cfg.History.MaxWorkers <= 0 {
		cfg.History.MaxWorkers = 6
	}

	if cfg.Risk.TakeProfitPct <= 0 {
		cfg.Risk.TakeProfitPct = 0.05
	}
	if cfg.Risk.StopLossPct <= 0 {
		cfg.Risk.StopLossPct = 0.02
	}

	if cfg.OpenAI.TTSModel == "" {
		cfg.OpenAI.TTSModel = "tts-1"
	}
	if cfg.OpenAI.Voice == "" {
		cfg.OpenAI.Voice = "alloy"
	}
	if cfg.OpenAI.ResponseFormat == "" {
		cfg.OpenAI.ResponseFormat = "mp3"
	}

	if cfg.UI.MaxEvents <= 0 {
		cfg.UI.MaxEvents = 250
	}
}

func validate(cfg *Config) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return errors.New("server.port must be 1..65535")
	}
	if cfg.Filters.Open5mRangePctMin <= 0 || cfg.Filters.Open5mRangePctMax <= 0 || cfg.Filters.Open5mRangePctMax < cfg.Filters.Open5mRangePctMin {
		return errors.New("filters.open_5m_range_pct_min/max invalid")
	}
	if cfg.Filters.Open5mVolMax < cfg.Filters.Open5mVolMin {
		return errors.New("filters.open_5m_vol_min/max invalid")
	}
	if cfg.Filters.EntryMaxAfterOpen < cfg.Filters.EntryMinAfterOpen {
		return errors.New("filters.entry_minutes_after_open_min/max invalid")
	}
	if cfg.Filters.EntryPriceMax < cfg.Filters.EntryPriceMin {
		return errors.New("filters.entry_price_min/max invalid")
	}
	return nil
}
