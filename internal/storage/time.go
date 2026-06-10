package storage

import "time"

func Now() time.Time {
	return time.Now()
}

func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func ParseOptionalTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := ParseTime(s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
