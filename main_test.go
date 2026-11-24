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
		{name: "upper bound", value: "135", want: 135},
		{name: "not an integer", value: "ninety", wantErr: "must be an integer"},
		{name: "above the actuation range", value: "136", wantErr: "must be between 0 and 135"},
		{name: "negative", value: "-1", wantErr: "must be between 0 and 135"},
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
