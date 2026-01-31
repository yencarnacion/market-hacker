package watchlist

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type WatchlistFile struct {
	Watchlist []struct {
		Symbol string `yaml:"symbol"`
	} `yaml:"watchlist"`
}

func Load(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wl WatchlistFile
	if err := yaml.Unmarshal(b, &wl); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(wl.Watchlist))
	seen := make(map[string]struct{}, len(wl.Watchlist))
	for _, it := range wl.Watchlist {
		s := strings.ToUpper(strings.TrimSpace(it.Symbol))
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}
