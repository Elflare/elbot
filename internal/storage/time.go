package storage

import (
	"time"

	"elbot/internal/storage/share"
)

func Now() time.Time {
	return time.Now()
}

func FormatTime(t time.Time) string                  { return share.FormatTime(t) }
func ParseTime(s string) (time.Time, error)          { return share.ParseTime(s) }
func ParseOptionalTime(s string) (*time.Time, error) { return share.ParseOptionalTime(s) }
