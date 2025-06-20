package clock

import (
	"fmt"
	"time"
)

func Now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// Duration duration between two times represented as strings
func Duration(from, to string) (time.Duration, error) {
	fromTime, err := time.Parse("2006-01-02T15:04:05Z", from)
	if err != nil {
		return 0, fmt.Errorf("from is not a valid time: %s", from)
	}
	toTime, err := time.Parse("2006-01-02T15:04:05Z", to)
	if err != nil {
		return 0, fmt.Errorf("to is not a valid time: %s", to)
	}
	return toTime.Sub(fromTime), nil
}

// DurationHours duration in hours between two times represented as strings,
// result value rounded to 3 decimal places
func DurationHours(from, to string) float64 {
	duration, err := Duration(from, to)
	if err != nil {
		return 0
	}
	return float64(int(duration.Hours()*1000)) / 1000
}
