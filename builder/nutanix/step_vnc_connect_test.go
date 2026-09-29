package nutanix

import "testing"

func TestConsoleHeaders(t *testing.T) {
	cases := []struct {
		name       string
		cc         ClusterConfig
		wantAPIKey string
		wantBasic  bool
	}{
		{"api key", ClusterConfig{APIKey: "k", Username: "u", Password: "p"}, "k", false},
		{"legacy api key username", ClusterConfig{Username: "X-ntnx-api-key", Password: "legacy-key"}, "legacy-key", false},
		{"basic auth", ClusterConfig{Username: "u", Password: "p"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cc.CustomHeaders = map[string]string{"Cf-Access-Client-Id": "cf-id"}
			s := &stepVNCConnect{Config: &Config{ClusterConfig: tc.cc}}
			h := s.consoleHeaders()

			if got := h.Get("X-ntnx-api-key"); got != tc.wantAPIKey {
				t.Errorf("X-ntnx-api-key = %q, want %q", got, tc.wantAPIKey)
			}
			if got := h.Get("Authorization"); (got != "") != tc.wantBasic {
				t.Errorf("Authorization = %q, want basic=%v", got, tc.wantBasic)
			}
			if got := h.Get("Cf-Access-Client-Id"); got != "cf-id" {
				t.Errorf("custom header = %q, want cf-id", got)
			}
		})
	}
}
