package server

import (
	"strconv"
	"time"
)

func itoa(i int) string { return strconv.Itoa(i) }

func mustLoc(name string) *time.Location {
	if name == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.Local
	}
	return loc
}
