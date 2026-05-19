package api

import (
	"testing"
	"time"
)

func TestDecodeUnixSecondsAcceptsRPCNumberShapes(t *testing.T) {
	tests := []struct {
		name string
		arg  any
		want time.Time
	}{
		{"float", float64(7.25), time.Unix(7, 250_000_000)},
		{"uint64", uint64(7), time.Unix(7, 0)},
		{"int64", int64(7), time.Unix(7, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeUnixSeconds([]any{"path", tt.arg}, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("decodeUnixSeconds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodeUnixSecondsRejectsInvalidArgs(t *testing.T) {
	if _, err := decodeUnixSeconds([]any{"path", "bad"}, 1); err == nil {
		t.Fatal("decodeUnixSeconds error = nil, want error")
	}
	if _, err := decodeUnixSeconds([]any{"path"}, 1); err == nil {
		t.Fatal("decodeUnixSeconds missing arg error = nil, want error")
	}
}
