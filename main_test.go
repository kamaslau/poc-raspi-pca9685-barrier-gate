package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAngleFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{name: "unset falls back to default", value: "", want: defaultOpenAngle},
		{name: "explicit value", value: "120", want: 120},
		{name: "lower bound", value: "0", want: 0},
		{name: "upper bound", value: "180", want: 180},
		{name: "not an integer", value: "ninety", wantErr: "must be an integer"},
		{name: "above the actuation range", value: "181", wantErr: "must be between 0 and 180"},
		{name: "negative", value: "-1", wantErr: "must be between 0 and 180"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GATE_OPEN_ANGLE", tc.value)

			got, err := openAngleFromEnv()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("want %d, got %d", tc.want, got)
			}
		})
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		retained bool
		want     string
		wantOK   bool
	}{
		{name: "open", payload: "open", want: "open", wantOK: true},
		{name: "close", payload: "close", want: "close", wantOK: true},
		{name: "surrounding whitespace", payload: " open\n", want: "open", wantOK: true},
		{name: "retained open is a replay", payload: "open", retained: true, wantOK: false},
		{name: "retained close is a replay", payload: "close", retained: true, wantOK: false},
		{name: "unknown command", payload: "raise", wantOK: false},
		{name: "empty", payload: "", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseCommand([]byte(tc.payload), tc.retained)
			if ok != tc.wantOK {
				t.Fatalf("want ok=%v, got %v", tc.wantOK, ok)
			}
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestHealthReportOmitsEmptySymptoms(t *testing.T) {
	ok, err := json.Marshal(healthReport{OK: true, ClientID: "device-00000000", At: "2026-11-24T00:00:00Z"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(ok), "symptoms") {
		t.Fatalf("healthy report must not carry a symptoms key: %s", ok)
	}

	failed, err := json.Marshal(healthReport{OK: false, Symptoms: []string{"i2c bus open failed"}, ClientID: "device-00000000", At: "2026-11-24T00:00:00Z"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(failed), `"symptoms":["i2c bus open failed"]`) {
		t.Fatalf("failing report must name the symptom: %s", failed)
	}
}
