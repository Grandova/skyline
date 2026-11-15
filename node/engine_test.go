package node_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/crypto/curve25519"
	"google.golang.org/protobuf/types/known/anypb"

	core "github.com/v2fly/v2ray-core/v5"
	"github.com/v2fly/v2ray-core/v5/app/dispatcher"
	"github.com/v2fly/v2ray-core/v5/app/proxyman"
	v2net "github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"github.com/v2fly/v2ray-core/v5/features/outbound"
	"github.com/v2fly/v2ray-core/v5/proxy/dokodemo"
	"github.com/v2fly/v2ray-core/v5/transport/internet"
	"github.com/v2fly/v2ray-core/v5/transport/internet/grpc"
	"github.com/v2fly/v2ray-core/v5/transport/internet/tcp"
	v2tls "github.com/v2fly/v2ray-core/v5/transport/internet/tls"

	"skyline/config"
	"skyline/mx"
	"skyline/node"
	"skyline/panel"
	"skyline/transportext"
)

func getFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate free tcp port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func startTCPEchoServer(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp echo: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

func dialViaMXClient(t *testing.T, serverPort int, uuid string, network string, ns panel.NetworkSettings, tlsMode int, serverName string, targetPort int, payload []byte) ([]byte, error) {
	t.Helper()
	forwardPort := getFreePort(t)

	streamCfg := &internet.StreamConfig{}
	switch network {
	case "tcp":
		streamCfg.ProtocolName = "tcp"
		streamCfg.TransportSettings = []*internet.TransportConfig{
			{
				ProtocolName: "tcp",
				Settings:     serial.ToTypedMessage(&tcp.Config{}),
			},
		}
	case "ws":
		tc, err := transportext.EncodeTransportSettings(transportext.ExtendedTransportSettings{
			Network: transportext.ProtocolWS,
			Path:    ns.Path,
			Host:    ns.Host,
		})
		if err != nil {
			return nil, err
		}
		streamCfg.ProtocolName = transportext.ProtocolWS
		streamCfg.TransportSettings = []*internet.TransportConfig{tc}
	case "grpc":
		streamCfg.ProtocolName = "gun"
		streamCfg.TransportSettings = []*internet.TransportConfig{
			{
				ProtocolName: "gun",
				Settings: serial.ToTypedMessage(&grpc.Config{
					ServiceName: ns.ServiceName,
				}),
			},
		}
	case "xhttp", "mc1", "mundordp":
		tc, err := transportext.EncodeTransportSettings(transportext.ExtendedTransportSettings{
			Network:  network,
			Path:     ns.Path,
			Host:     ns.Host,
			Username: ns.Username,
		})
		if err != nil {
			return nil, err
		}
		streamCfg.ProtocolName = network
		streamCfg.TransportSettings = []*internet.TransportConfig{tc}
	}

	if tlsMode == 1 || tlsMode == 2 {
		tlsCfg := &v2tls.Config{
			AllowInsecure: true,
			ServerName:    serverName,
		}
		streamCfg.SecurityType = serial.GetMessageType(tlsCfg)
		streamCfg.SecuritySettings = []*anypb.Any{serial.ToTypedMessage(tlsCfg)}
	}

	clientCoreCfg := &core.Config{
		App: []*anypb.Any{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag: "doko-in",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: v2net.SinglePortRange(v2net.Port(forwardPort)),
					Listen:    v2net.NewIPOrDomain(v2net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address:  v2net.NewIPOrDomain(v2net.LocalHostIP),
					Port:     uint32(targetPort),
					Networks: []v2net.Network{v2net.Network_TCP},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				Tag: "mx-out",
				SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{
					StreamSettings: streamCfg,
				}),
				ProxySettings: serial.ToTypedMessage(&mx.ConfiguracionCliente{
					Vnext: []*protocol.ServerEndpoint{
						{
							Address: v2net.NewIPOrDomain(v2net.LocalHostIP),
							Port:    uint32(serverPort),
							User: []*protocol.User{
								{
									Level:   0,
									Email:   "client@mx",
									Account: serial.ToTypedMessage(&mx.Cuenta{Id: uuid}),
								},
							},
						},
					},
				}),
			},
		},
	}

	inst, err := core.New(clientCoreCfg)
	if err != nil {
		return nil, err
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		return nil, err
	}
	defer inst.Close()

	// Ensure outbound manager is initialized
	_ = inst.GetFeature(outbound.ManagerType())

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", forwardPort), 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

	if _, err := conn.Write(payload); err != nil {
		return nil, err
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, err
	}
	return reply, nil
}

func TestEndToEndAllTransportsAndSecurityModes(t *testing.T) {
	echoPort, stopEcho := startTCPEchoServer(t)
	defer stopEcho()

	privBytes := make([]byte, 32)
	_, _ = rand.Read(privBytes)
	pubBytes, _ := curve25519.X25519(privBytes, curve25519.Basepoint)
	privB64 := base64.RawURLEncoding.EncodeToString(privBytes)
	pubB64 := base64.RawURLEncoding.EncodeToString(pubBytes)

	cases := []struct {
		name    string
		network string
		tlsMode int
		ns      panel.NetworkSettings
		ts      panel.TLSSettings
	}{
		{
			name:    "TCP_Plain",
			network: "tcp",
			tlsMode: 0,
		},
		{
			name:    "TCP_TLS",
			network: "tcp",
			tlsMode: 1,
			ts:      panel.TLSSettings{ServerName: "mx.example.com", AllowInsecure: true},
		},
		{
			name:    "TCP_REALITY",
			network: "tcp",
			tlsMode: 2,
			ts: panel.TLSSettings{
				ServerName: "www.microsoft.com",
				PrivateKey: privB64,
				PublicKey:  pubB64,
				ShortID:    "a1b2c3d4",
				ServerPort: 443,
			},
		},
		{
			name:    "WS_Plain_FastAuth",
			network: "ws",
			tlsMode: 0,
			ns:      panel.NetworkSettings{Path: "/mx-ws", Host: "ws.example.com"},
		},
		{
			name:    "WS_TLS_FastAuth",
			network: "ws",
			tlsMode: 1,
			ns:      panel.NetworkSettings{Path: "/mx-wss"},
			ts:      panel.TLSSettings{ServerName: "ws.example.com", AllowInsecure: true},
		},
		{
			name:    "gRPC_Plain",
			network: "grpc",
			tlsMode: 0,
			ns:      panel.NetworkSettings{ServiceName: "MxTunService"},
		},
		{
			name:    "XHTTP_FastAuth",
			network: "xhttp",
			tlsMode: 0,
			ns:      panel.NetworkSettings{Path: "/xhttp-endpoint", Host: "cdn.example.com"},
		},
		{
			name:    "MC1_FastAuth",
			network: "mc1",
			tlsMode: 1,
			ns:      panel.NetworkSettings{Path: "/mc1-stream", Mode: "auto", CIDRSegments: panel.StringSlice{"10.0.0.0/8"}},
			ts:      panel.TLSSettings{ServerName: "mc1.example.com", AllowInsecure: true},
		},
		{
			name:    "MundoRDP_X224_And_TLS",
			network: "mundordp",
			tlsMode: 1,
			ns: panel.NetworkSettings{
				Username:    "AdminUser",
				FakeTitle:   "Corp RDP Portal",
				FakeMessage: "Authorized Access Only",
			},
			ts: panel.TLSSettings{ServerName: "rdp.example.com", AllowInsecure: true},
		},
	}

	userUUID := "11111111-2222-3333-4444-555555555555"
	users := []panel.User{
		{ID: 42, UUID: userUUID, SpeedLimit: 0, DeviceLimit: 0},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverPort := getFreePort(t)
			engine := node.NewNodeEngine(node.EngineOptions{
				NodeID:   100 + i,
				LogLevel: "none",
				ListenIP: "127.0.0.1",
			})
			defer engine.Close()

			nodeCfg := &panel.NodeConfig{
				Host:            "127.0.0.1",
				ListenIP:        "127.0.0.1",
				ServerPort:      serverPort,
				Network:         tc.network,
				NetworkSettings: tc.ns,
				Protocol:        "mx",
				TLS:             tc.tlsMode,
				TLSSettings:     tc.ts,
				ServerName:      tc.ts.ServerName,
			}

			restarted, err := engine.ApplyConfig(nodeCfg, users)
			if err != nil {
				t.Fatalf("ApplyConfig failed: %v", err)
			}
			if !restarted {
				t.Fatalf("expected initial ApplyConfig to start core")
			}

			msg := []byte("hello-skyline-mx-" + tc.name + "-" + strings.Repeat("X", 512))
			reply, err := dialViaMXClient(t, serverPort, userUUID, tc.network, tc.ns, tc.tlsMode, tc.ts.ServerName, echoPort, msg)
			if err != nil {
				t.Fatalf("dialViaMXClient failed for %s: %v", tc.name, err)
			}
			if !bytes.Equal(reply, msg) {
				t.Fatalf("echo mismatch: got %d bytes, want %d bytes", len(reply), len(msg))
			}

			traffic := engine.CollectAndResetTraffic()
			userTraffic, ok := traffic[42]
			if !ok {
				t.Fatalf("expected traffic recorded for user 42, got %v", traffic)
			}
			if userTraffic[0] < int64(len(msg)) || userTraffic[1] < int64(len(msg)) {
				t.Fatalf("unexpected traffic counts for user 42: up=%d, down=%d (want >= %d)", userTraffic[0], userTraffic[1], len(msg))
			}
		})
	}
}

func TestXboardUniProxyWorkflowAndDynamicUserSync(t *testing.T) {
	echoPort, stopEcho := startTCPEchoServer(t)
	defer stopEcho()

	nodePort := getFreePort(t)
	const (
		validToken = "xboard-secret-token"
		nodeID     = 7
		user1UUID  = "aaaa1111-2222-3333-4444-555555555555"
		user2UUID  = "bbbb1111-2222-3333-4444-555555555555"
	)

	var (
		mu             sync.Mutex
		currentUsers   = []map[string]any{{"id": 1, "uuid": user1UUID, "speed_limit": 10, "device_limit": 1}}
		pushedTraffic  = make(map[string][2]int64)
		pushedAlive    = make(map[string][]string)
		pushedStatuses []panel.NodeStatus
		aliveListCount = map[string]int{}
		config304Count int
		user304Count   int
	)

	// Mock Xboard server matching MxProtocol + UniProxyController contract (including PHP empty array "[]" serialization)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != validToken || r.URL.Query().Get("node_type") != "mx" || r.URL.Query().Get("node_id") != strconv.Itoa(nodeID) {
			http.Error(w, "unauthorized", http.StatusForbidden)
			return
		}
		// Simulate PHP empty array serialization `"network_settings": []` and `"tls_settings": []`
		rawJSON := fmt.Sprintf(`{
			"host": "127.0.0.1",
			"listen_ip": "127.0.0.1",
			"server_port": %d,
			"network": "ws",
			"networkSettings": {"path": "/uni-ws"},
			"network_settings": {"path": "/uni-ws"},
			"protocol": "mx",
			"tls": 0,
			"tls_settings": [],
			"tlsSettings": [],
			"server_name": null,
			"allow_insecure": 0,
			"base_config": {"push_interval": 60, "pull_interval": 60},
			"routes": [{"id": 1, "match": ["blocked.example.com"], "action": "block", "action_value": ""}]
		}`, nodePort)
		etag := `"cfg-etag-v1"`
		if r.Header.Get("If-None-Match") == etag {
			mu.Lock()
			config304Count++
			mu.Unlock()
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(rawJSON))
	})

	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		usersBytes, _ := json.Marshal(map[string]any{"users": currentUsers})
		mu.Unlock()
		etag := fmt.Sprintf(`"usr-%d"`, len(usersBytes))
		if r.Header.Get("If-None-Match") == etag {
			mu.Lock()
			user304Count++
			mu.Unlock()
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(usersBytes)
	})

	mux.HandleFunc("/api/v1/server/UniProxy/alivelist", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		raw, _ := json.Marshal(map[string]any{"alive": aliveListCount})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})

	mux.HandleFunc("/api/v1/server/UniProxy/push", func(w http.ResponseWriter, r *http.Request) {
		var data map[string][2]int64
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		mu.Lock()
		for uid, pair := range data {
			cur := pushedTraffic[uid]
			cur[0] += pair[0]
			cur[1] += pair[1]
			pushedTraffic[uid] = cur
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":true}`))
	})

	mux.HandleFunc("/api/v1/server/UniProxy/alive", func(w http.ResponseWriter, r *http.Request) {
		var data map[string][]string
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for uid, ips := range data {
			pushedAlive[uid] = ips
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":true}`))
	})

	mux.HandleFunc("/api/v1/server/UniProxy/status", func(w http.ResponseWriter, r *http.Request) {
		var st panel.NodeStatus
		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		pushedStatuses = append(pushedStatuses, st)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":true,"code":0,"message":"success"}`))
	})

	panelServer := httptest.NewServer(mux)
	defer panelServer.Close()

	worker := node.NewNodeWorker(config.NodeTargetConfig{
		LogLevel:     "none",
		PanelURL:     panelServer.URL,
		Token:        validToken,
		NodeID:       nodeID,
		NodeType:     "mx",
		APIVersion:   "v1",
		PullInterval: time.Hour,
		PushInterval: time.Hour,
		HTTPTimeout:  5 * time.Second,
		DeviceTTL:    time.Minute,
	}, log.New(io.Discard, "", 0))
	defer worker.Stop()

	ctx := context.Background()
	if err := worker.SyncOnce(ctx); err != nil {
		t.Fatalf("first SyncOnce failed: %v", err)
	}

	// Second SyncOnce should hit ETag 304 Not Modified for both config and user
	if err := worker.SyncOnce(ctx); err != nil {
		t.Fatalf("second SyncOnce failed: %v", err)
	}
	mu.Lock()
	if config304Count != 1 || user304Count != 1 {
		t.Fatalf("expected ETag 304 hit for config (%d) and user (%d)", config304Count, user304Count)
	}
	mu.Unlock()

	// User 1 should proxy successfully
	payload := []byte("user1-payload-data-over-ws-mx")
	reply, err := dialViaMXClient(t, nodePort, user1UUID, "ws", panel.NetworkSettings{Path: "/uni-ws"}, 0, "", echoPort, payload)
	if err != nil {
		t.Fatalf("User 1 proxy failed: %v", err)
	}
	if !bytes.Equal(reply, payload) {
		t.Fatalf("User 1 reply mismatch")
	}

	// User 2 (not yet added) should fail authentication
	if _, err := dialViaMXClient(t, nodePort, user2UUID, "ws", panel.NetworkSettings{Path: "/uni-ws"}, 0, "", echoPort, payload); err == nil {
		t.Fatalf("expected unauthenticated User 2 to fail before sync")
	}

	// ReportOnce should push User 1 traffic, alive IPs, and node status to Xboard
	if err := worker.ReportOnce(ctx); err != nil {
		t.Fatalf("ReportOnce failed: %v", err)
	}

	mu.Lock()
	u1Traffic, hasU1 := pushedTraffic["1"]
	u1Alive := pushedAlive["1"]
	statusCount := len(pushedStatuses)
	mu.Unlock()

	if !hasU1 || u1Traffic[0] < int64(len(payload)) || u1Traffic[1] < int64(len(payload)) {
		t.Fatalf("expected pushed traffic for user 1 >= %d, got %v", len(payload), u1Traffic)
	}
	if len(u1Alive) == 0 {
		t.Fatalf("expected alive IP reported for user 1")
	}
	if statusCount == 0 || pushedStatuses[0].Mem.Total == 0 {
		t.Fatalf("expected valid node status reported, got %+v", pushedStatuses)
	}

	// Now hot-reload user list: remove User 1, add User 2
	mu.Lock()
	currentUsers = []map[string]any{
		{"id": 2, "uuid": user2UUID, "speed_limit": nil, "device_limit": nil},
	}
	mu.Unlock()

	if err := worker.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after user change failed: %v", err)
	}

	// User 2 should now succeed without listener restart!
	reply2, err := dialViaMXClient(t, nodePort, user2UUID, "ws", panel.NetworkSettings{Path: "/uni-ws"}, 0, "", echoPort, payload)
	if err != nil {
		t.Fatalf("User 2 proxy failed after hot-sync: %v", err)
	}
	if !bytes.Equal(reply2, payload) {
		t.Fatalf("User 2 reply mismatch")
	}

	// Removed User 1 should now be rejected
	if _, err := dialViaMXClient(t, nodePort, user1UUID, "ws", panel.NetworkSettings{Path: "/uni-ws"}, 0, "", echoPort, payload); err == nil {
		t.Fatalf("expected removed User 1 to be rejected after hot-sync")
	}
}

func TestDeviceLimitAndRouteBlocking(t *testing.T) {
	tracker := node.NewNodeTracker(99, "mx-node-99", time.Minute)
	tracker.UpdateUsers([]panel.User{
		{ID: 10, UUID: "u10", DeviceLimit: 1},
	})
	tracker.SetRoutes([]panel.RouteRule{
		{ID: 1, Match: panel.StringSlice{"*.blocked.test", "10.99.0.0/16"}, Action: "block"},
	})

	email := node.FormatUserEmail(99, 10)

	// First IP (1.1.1.1) is allowed under device_limit=1
	_, release1, err := tracker.AcquireConnection(email, "1.1.1.1:50000")
	if err != nil {
		t.Fatalf("expected first IP 1.1.1.1 to be allowed: %v", err)
	}
	defer release1()

	// Second connection from same IP (1.1.1.1) is allowed
	_, release2, err := tracker.AcquireConnection(email, "1.1.1.1:50001")
	if err != nil {
		t.Fatalf("expected second conn from same IP 1.1.1.1 to be allowed: %v", err)
	}
	defer release2()

	// Connection from second distinct IP (2.2.2.2) exceeds device_limit=1 and must be rejected
	if _, _, err := tracker.AcquireConnection(email, "2.2.2.2:60000"); err == nil {
		t.Fatalf("expected second distinct IP 2.2.2.2 to be rejected when device_limit=1")
	}

	// Verify route blocking
	action, _ := tracker.MatchRoute(v2net.TCPDestination(v2net.ParseAddress("api.blocked.test"), 443), "tcp")
	if action != "block" {
		t.Fatalf("expected domain api.blocked.test to match block route, got %q", action)
	}
	action, _ = tracker.MatchRoute(v2net.TCPDestination(v2net.ParseAddress("10.99.1.25"), 80), "tcp")
	if action != "block" {
		t.Fatalf("expected IP 10.99.1.25 to match block CIDR route, got %q", action)
	}
	action, _ = tracker.MatchRoute(v2net.TCPDestination(v2net.ParseAddress("allowed.example.com"), 443), "tcp")
	if action != "" {
		t.Fatalf("expected allowed.example.com to pass, got %q", action)
	}
}

func TestProxyProtocolV1AndV2SendAndReceive(t *testing.T) {
	// 1. Unit verification of Proxy Protocol v1 and v2 wire headers
	for _, ver := range []int{1, 2} {
		var buf bytes.Buffer
		src := &net.TCPAddr{IP: net.ParseIP("203.0.113.88"), Port: 54321}
		dst := &net.TCPAddr{IP: net.ParseIP("198.51.100.10"), Port: 443}
		if err := transportext.WriteProxyProtocolHeader(&buf, ver, src, dst); err != nil {
			t.Fatalf("WriteProxyProtocolHeader(v%d) failed: %v", ver, err)
		}
		hdr, err := proxyproto.Read(bufio.NewReader(&buf))
		if err != nil {
			t.Fatalf("proxyproto.Read(v%d) failed: %v", ver, err)
		}
		if int(hdr.Version) != ver {
			t.Fatalf("expected header version %d, got %d", ver, hdr.Version)
		}
		gotSrc, ok := hdr.SourceAddr.(*net.TCPAddr)
		if !ok || !gotSrc.IP.Equal(src.IP) || gotSrc.Port != src.Port {
			t.Fatalf("v%d source addr mismatch: got %v, want %v", ver, hdr.SourceAddr, src)
		}
	}

	// 2. End-to-End verification: Inbound Proxy Protocol (v1 & v2) -> MX Node -> Outbound Proxy Protocol (v1 & v2)
	for _, ver := range []int{1, 2} {
		t.Run(fmt.Sprintf("E2E_ProxyProtocol_v%d", ver), func(t *testing.T) {
			rawLn, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen downstream: %v", err)
			}
			defer rawLn.Close()
			downstreamPort := rawLn.Addr().(*net.TCPAddr).Port

			var (
				observedVer  byte
				observedSrc  string
				observedMu   sync.Mutex
				downstreamCh = make(chan struct{}, 1)
			)

			go func() {
				for {
					c, err := rawLn.Accept()
					if err != nil {
						return
					}
					go func(conn net.Conn) {
						defer conn.Close()
						br := bufio.NewReader(conn)
						hdr, err := proxyproto.Read(br)
						if err != nil {
							return
						}
						observedMu.Lock()
						observedVer = hdr.Version
						if hdr.SourceAddr != nil {
							observedSrc = hdr.SourceAddr.String()
						}
						observedMu.Unlock()
						select {
						case downstreamCh <- struct{}{}:
						default:
						}
						_, _ = io.Copy(conn, br)
					}(c)
				}
			}()

			serverPort := getFreePort(t)
			acceptTrue := true
			engine := node.NewNodeEngine(node.EngineOptions{
				NodeID:              300 + ver,
				LogLevel:            "none",
				ListenIP:            "127.0.0.1",
				ProxyProtocol:       ver,
				AcceptProxyProtocol: &acceptTrue,
			})
			defer engine.Close()

			userUUID := "99999999-8888-7777-6666-555555555555"
			users := []panel.User{{ID: 77, UUID: userUUID}}
			nodeCfg := &panel.NodeConfig{
				Host:       "127.0.0.1",
				ListenIP:   "127.0.0.1",
				ServerPort: serverPort,
				Network:    "ws",
				NetworkSettings: panel.NetworkSettings{
					Path:                "/pp-ws",
					AcceptProxyProtocol: true,
					ProxyProtocol:       panel.FlexibleProxyProtocol(ver),
				},
				Protocol: "mx",
				TLS:      0,
			}

			if _, err := engine.ApplyConfig(nodeCfg, users); err != nil {
				t.Fatalf("ApplyConfig failed: %v", err)
			}

			msg := []byte(fmt.Sprintf("proxy-protocol-v%d-payload", ver))
			ns := panel.NetworkSettings{
				Path:          "/pp-ws",
				ProxyProtocol: panel.FlexibleProxyProtocol(ver),
			}
			// Dial via MX client which also sends Proxy Protocol header on its dialer
			tc, err := transportext.EncodeTransportSettings(transportext.ExtendedTransportSettings{
				Network:       transportext.ProtocolWS,
				Path:          ns.Path,
				ProxyProtocol: ver,
			})
			if err != nil {
				t.Fatalf("EncodeTransportSettings: %v", err)
			}

			forwardPort := getFreePort(t)
			clientCoreCfg := &core.Config{
				App: []*anypb.Any{
					serial.ToTypedMessage(&dispatcher.Config{}),
					serial.ToTypedMessage(&proxyman.InboundConfig{}),
					serial.ToTypedMessage(&proxyman.OutboundConfig{}),
				},
				Inbound: []*core.InboundHandlerConfig{
					{
						Tag: "doko-in",
						ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
							PortRange: v2net.SinglePortRange(v2net.Port(forwardPort)),
							Listen:    v2net.NewIPOrDomain(v2net.LocalHostIP),
						}),
						ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
							Address:  v2net.NewIPOrDomain(v2net.LocalHostIP),
							Port:     uint32(downstreamPort),
							Networks: []v2net.Network{v2net.Network_TCP},
						}),
					},
				},
				Outbound: []*core.OutboundHandlerConfig{
					{
						Tag: "mx-out",
						SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{
							StreamSettings: &internet.StreamConfig{
								ProtocolName:      transportext.ProtocolWS,
								TransportSettings: []*internet.TransportConfig{tc},
							},
						}),
						ProxySettings: serial.ToTypedMessage(&mx.ConfiguracionCliente{
							Vnext: []*protocol.ServerEndpoint{
								{
									Address: v2net.NewIPOrDomain(v2net.LocalHostIP),
									Port:    uint32(serverPort),
									User: []*protocol.User{
										{
											Level:   0,
											Email:   "client@mx",
											Account: serial.ToTypedMessage(&mx.Cuenta{Id: userUUID}),
										},
									},
								},
							},
						}),
					},
				},
			}

			inst, err := core.New(clientCoreCfg)
			if err != nil {
				t.Fatalf("core.New: %v", err)
			}
			if err := inst.Start(); err != nil {
				_ = inst.Close()
				t.Fatalf("inst.Start: %v", err)
			}
			defer inst.Close()

			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", forwardPort), 3*time.Second)
			if err != nil {
				t.Fatalf("dial forwardPort: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(4 * time.Second))

			if _, err := conn.Write(msg); err != nil {
				t.Fatalf("write: %v", err)
			}
			reply := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, reply); err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(reply, msg) {
				t.Fatalf("echo mismatch: got %q, want %q", reply, msg)
			}

			select {
			case <-downstreamCh:
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out waiting for downstream Proxy Protocol v%d header", ver)
			}

			observedMu.Lock()
			gotVer := int(observedVer)
			gotSrc := observedSrc
			observedMu.Unlock()

			if gotVer != ver {
				t.Fatalf("downstream received Proxy Protocol version %d, want %d", gotVer, ver)
			}
			if !strings.HasPrefix(gotSrc, "127.0.0.1:") {
				t.Fatalf("downstream received unexpected source address %q", gotSrc)
			}
		})
	}
}
