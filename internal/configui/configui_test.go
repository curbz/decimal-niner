package configui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAPIReadAndPreserveUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte("d9:\n  traffic_engine: vat\n  future_setting: true # keep me\nxplane_api:\n  web_api_http_url: http://127.0.0.1:8086/api/v2\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	s := New(path, dir)
	get := httptest.NewRecorder()
	s.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", get.Code, get.Body.String())
	}

	payload := map[string]any{"config": map[string]any{
		"d9":         map[string]any{"traffic_engine": "d9traffic", "future_setting": true},
		"xplane_api": map[string]any{"web_api_http_url": "http://127.0.0.1:8086/api/v2"},
	}}
	body, _ := json.Marshal(payload)
	put := httptest.NewRecorder()
	s.Handler().ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body)))
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", put.Code, put.Body.String())
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "future_setting: true") || !strings.Contains(string(saved), "traffic_engine: d9traffic") {
		t.Fatalf("saved config lost values:\n%s", saved)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]any
		wantErr bool
	}{
		{name: "valid engine", config: map[string]any{"d9": map[string]any{"traffic_engine": "vat"}}},
		{name: "invalid engine", config: map[string]any{"d9": map[string]any{"traffic_engine": "unknown"}}, wantErr: true},
		{name: "invalid refresh", config: map[string]any{"vat": map[string]any{"refresh_interval_seconds": float64(-1)}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
