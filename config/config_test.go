package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"skyline/config"
)

func TestLoadYAMLAndJSONConfig(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yml")
	yamlContent := `
log_level: debug
panel_url: https://xboard.example.com/
token: secret-token
node_id: 12
node_type: MX
api_version: v1
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	cfg, err := config.Load(yamlPath)
	if err != nil {
		t.Fatalf("Load yaml failed: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate yaml failed: %v", err)
	}
	if len(cfg.Nodes) != 1 || cfg.Nodes[0].NodeID != 12 || cfg.Nodes[0].PanelURL != "https://xboard.example.com" || cfg.Nodes[0].NodeType != "mx" {
		t.Fatalf("unexpected normalized nodes: %+v", cfg.Nodes)
	}

	jsonPath := filepath.Join(tmpDir, "config.json")
	jsonContent := `{
		"api_host": "https://panel2.example.com/",
		"api_key": "key-2",
		"nodes": [
			{"node_id": 1},
			{"node_id": 2, "listen_ip": "127.0.0.1"}
		]
	}`
	if err := os.WriteFile(jsonPath, []byte(jsonContent), 0600); err != nil {
		t.Fatalf("write json: %v", err)
	}

	cfg2, err := config.Load(jsonPath)
	if err != nil {
		t.Fatalf("Load json failed: %v", err)
	}
	if err := cfg2.Validate(); err != nil {
		t.Fatalf("Validate json failed: %v", err)
	}
	if len(cfg2.Nodes) != 2 || cfg2.Nodes[0].PanelURL != "https://panel2.example.com" || cfg2.Nodes[1].Token != "key-2" {
		t.Fatalf("unexpected multi-node inheritance: %+v", cfg2.Nodes)
	}
}
