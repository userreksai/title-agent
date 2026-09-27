package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMasterDefaultsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.json")
	minimal := `{"agents":[{"name":"a","url":"http://127.0.0.1:8003","token":"real-test-token-123456"}],"seo":{"base_url":"http://127.0.0.1:8080"}}`
	if err := os.WriteFile(path, []byte(minimal), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ReadMaster(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.IntervalSeconds != 600 || c.FailurePercent != 100 || c.TitleTimeoutSeconds != 15 || c.PerAgentConcurrency != 20 {
		t.Fatalf("defaults %+v", c)
	}
	if !filepath.IsAbs(c.StateFile) {
		t.Fatal("state path not resolved relative to config")
	}
	for _, field := range []string{"failure_percent", "title_timeout_seconds", "min_healthy_agents", "per_agent_concurrency"} {
		t.Run(field, func(t *testing.T) {
			var raw map[string]any
			_ = json.Unmarshal([]byte(minimal), &raw)
			raw[field] = 0
			b, _ := json.Marshal(raw)
			_ = os.WriteFile(path, b, 0600)
			if _, err = ReadMaster(path); err == nil {
				t.Fatal("accepted zero " + field)
			}
		})
	}
}

func TestRejectUnknownConfigAndUnsafeAgentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	for _, raw := range []string{`{"token":"CHANGE_ME_TO_A_RANDOM_32_CHARACTER_TOKEN"}`, `{"token":"real-test-token-123456","max_timeout_seconds":16}`, `{"token":"real-test-token-123456","typo":true}`} {
		_ = os.WriteFile(path, []byte(raw), 0600)
		if _, err := ReadAgent(path); err == nil {
			t.Fatalf("accepted config %s", raw)
		}
	}
}
