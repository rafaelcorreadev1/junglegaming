package application

import (
	"errors"
	"math"
	"testing"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

func TestCursorRoundTrip(t *testing.T) {
	for _, sequence := range []int64{1, 42, math.MaxInt64} {
		cursor := encodeCursor(sequence)
		got, err := decodeCursor(cursor)
		if err != nil || got != sequence {
			t.Errorf("decodeCursor(%q) = %d, %v; want %d", cursor, got, err, sequence)
		}
	}

	if got, err := decodeCursor(""); err != nil || got != 0 {
		t.Errorf("an empty cursor must mean the first page, got %d, %v", got, err)
	}
}

func TestDecodeCursorRejectsWhatTheAPIDidNotIssue(t *testing.T) {
	cursors := map[string]string{
		"not base64":        "###",
		"raw number":        "42",
		"missing prefix":    "NDI",                             // "42"
		"another version":   "djI6NDI",                         // "v2:42"
		"not a number":      "djE6YWJj",                        // "v1:abc"
		"negative":          "djE6LTE",                         // "v1:-1"
		"padded base64":     "djE6NDI=",                        // "v1:42" with padding
		"overflowing int64": "djE6OTk5OTk5OTk5OTk5OTk5OTk5OTk", // "v1:99999999999999999999"
	}
	for name, cursor := range cursors {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCursor(cursor)
			var validation *domain.ValidationError
			if !errors.As(err, &validation) || validation.Field != "cursor" {
				t.Errorf("decodeCursor(%q) error = %v, want a validation error on cursor", cursor, err)
			}
		})
	}
}
