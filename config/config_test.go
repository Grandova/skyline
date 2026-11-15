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

	ppYamlPath := filepath.Join(tmpDir, "config-pp.yml")
	ppYamlContent := `
panel_url: https://xboard.example.com
token: secret-token
proxy_protocol: v2
accept_proxy_protocol: true
forward_address: 127.0.0.1:8443
nodes:
  - node_id: 1
    proxy_protocol: v1
  - node_id: 2
`
	if err := os.WriteFile(ppYamlPath, []byte(ppYamlContent), 0600); err != nil {
		t.Fatalf("write pp yaml: %v", err)
	}
	cfg3, err := config.Load(ppYamlPath)
	if err != nil {
		t.Fatalf("Load pp yaml failed: %v", err)
	}
	if err := cfg3.Validate(); err != nil {
		t.Fatalf("Validate pp yaml failed: %v", err)
	}
	if cfg3.Nodes[0].ProxyProtocol != 1 || cfg3.Nodes[1].ProxyProtocol != 2 {
		t.Fatalf("unexpected node proxy_protocol values: node0=%d, node1=%d", cfg3.Nodes[0].ProxyProtocol, cfg3.Nodes[1].ProxyProtocol)
	}
	if cfg3.Nodes[1].AcceptProxyProtocol == nil || !*cfg3.Nodes[1].AcceptProxyProtocol || cfg3.Nodes[1].ForwardAddress != "127.0.0.1:8443" {
		t.Fatalf("unexpected node1 proxy protocol inheritance: %+v", cfg3.Nodes[1])
	}
}
