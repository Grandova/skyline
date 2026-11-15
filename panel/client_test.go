package panel_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"skyline/panel"
)

func TestPanelClientV2ReportAndMachineDiscovery(t *testing.T) {
	var gotReport panel.NodeReport
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/server/machine/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[{"id":5,"type":"mx","name":"MX-HK"}],"base_config":{"push_interval":30,"pull_interval":45}}`))
	})
	mux.HandleFunc("/api/v2/server/report", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReport)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":true}`))
	})
	mux.HandleFunc("/api/v2/server/alivelist", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Test empty PHP array "[]" for alive
		_, _ = w.Write([]byte(`{"alive":[]}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := panel.NewClient(panel.ClientOptions{
		PanelURL:   srv.URL,
		Token:      "machine-tok",
		NodeID:     5,
		NodeType:   "mx",
		MachineID:  3,
		APIVersion: "v2",
	})

	ctx := context.Background()
	mNodes, err := c.GetMachineNodes(ctx)
	if err != nil {
		t.Fatalf("GetMachineNodes failed: %v", err)
	}
	if len(mNodes.Nodes) != 1 || mNodes.Nodes[0].ID != 5 {
		t.Fatalf("unexpected machine nodes: %+v", mNodes)
	}

	alive, err := c.GetAliveList(ctx)
	if err != nil || len(alive) != 0 {
		t.Fatalf("expected empty alive map from PHP [], got %v (err=%v)", alive, err)
	}

	err = c.PushReport(ctx, panel.NodeReport{
		Traffic: map[string][2]int64{"10": {1024, 2048}},
		Alive:   map[string][]string{"10": {"1.2.3.4"}},
		Online:  map[string]int{"10": 2},
	})
	if err != nil {
		t.Fatalf("PushReport failed: %v", err)
	}
	if gotReport.Traffic["10"] != [2]int64{1024, 2048} || gotReport.Online["10"] != 2 {
		t.Fatalf("unexpected report received by server: %+v", gotReport)
	}
}
