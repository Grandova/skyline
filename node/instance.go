package node

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	core "github.com/v2fly/v2ray-core/v5"
	applog "github.com/v2fly/v2ray-core/v5/app/log"
	"github.com/v2fly/v2ray-core/v5/app/policy"
	"github.com/v2fly/v2ray-core/v5/app/proxyman"
	_ "github.com/v2fly/v2ray-core/v5/app/proxyman/inbound"
	_ "github.com/v2fly/v2ray-core/v5/app/proxyman/outbound"
	appstats "github.com/v2fly/v2ray-core/v5/app/stats"
	commlog "github.com/v2fly/v2ray-core/v5/common/log"
	v2net "github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"github.com/v2fly/v2ray-core/v5/features/inbound"
	"github.com/v2fly/v2ray-core/v5/features/stats"
	"github.com/v2fly/v2ray-core/v5/proxy"
	"github.com/v2fly/v2ray-core/v5/proxy/blackhole"
	"github.com/v2fly/v2ray-core/v5/proxy/freedom"
	"github.com/v2fly/v2ray-core/v5/transport/internet"
	"github.com/v2fly/v2ray-core/v5/transport/internet/grpc"
	"github.com/v2fly/v2ray-core/v5/transport/internet/tcp"
	_ "github.com/v2fly/v2ray-core/v5/transport/internet/tls"
	_ "github.com/v2fly/v2ray-core/v5/transport/internet/udp"
	_ "github.com/v2fly/v2ray-core/v5/transport/internet/websocket"

	"skyline/mx"
	"skyline/panel"
	"skyline/transportext"
)

// EngineOptions holds local overrides for a node engine.
type EngineOptions struct {
	NodeID    int
	LogLevel  string
	ListenIP  string
	CertFile  string
	KeyFile   string
	DeviceTTL time.Duration
}

// NodeEngine manages the V2Ray core.Instance and MX protocol inbound for a single Xboard node.
type NodeEngine struct {
	opts    EngineOptions
	tag     string
	tracker *NodeTracker

	mu             sync.Mutex
	instance       *core.Instance
	statsMgr       stats.Manager
	currentCfg     *panel.NodeConfig
	currentUsers   map[int]panel.User
	pendingTraffic map[int][2]int64
}

// NewNodeEngine creates a new NodeEngine for the specified node ID.
func NewNodeEngine(opts EngineOptions) *NodeEngine {
	tag := fmt.Sprintf("mx-node-%d", opts.NodeID)
	tracker := NewNodeTracker(opts.NodeID, tag, opts.DeviceTTL)
	RegisterTracker(tag, tracker)
	return &NodeEngine{
		opts:           opts,
		tag:            tag,
		tracker:        tracker,
		currentUsers:   make(map[int]panel.User),
		pendingTraffic: make(map[int][2]int64),
	}
}

// Tracker returns the NodeTracker associated with this engine.
func (e *NodeEngine) Tracker() *NodeTracker {
	return e.tracker
}

// IsRunning returns whether the underlying V2Ray core instance is currently active.
func (e *NodeEngine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.instance != nil
}

// ApplyConfig starts or restarts the V2Ray core instance if the node configuration changed.
// Returns (restarted, error).
func (e *NodeEngine) ApplyConfig(nodeCfg *panel.NodeConfig, users []panel.User) (bool, error) {
	if nodeCfg == nil {
		return false, fmt.Errorf("nil node config")
	}
	nodeCfg.Normalize()

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.instance != nil && e.currentCfg != nil && !needsCoreRestart(e.currentCfg, nodeCfg) {
		// Only route rules may have changed; update routes in-place without restarting listener
		e.tracker.SetRoutes(nodeCfg.Routes)
		e.currentCfg = cloneNodeConfig(nodeCfg)
		return false, nil
	}

	// Flush any uncollected traffic into pendingTraffic before stopping the old instance
	if e.instance != nil {
		e.collectTrafficLocked()
		_ = e.instance.Close()
		e.instance = nil
		e.statsMgr = nil
	}

	e.tracker.SetRoutes(nodeCfg.Routes)
	e.tracker.UpdateUsers(users)

	coreCfg, err := e.buildCoreConfig(nodeCfg, users)
	if err != nil {
		return false, fmt.Errorf("build core config for node %d: %w", e.opts.NodeID, err)
	}

	inst, err := core.New(coreCfg)
	if err != nil {
		return false, fmt.Errorf("create core instance for node %d: %w", e.opts.NodeID, err)
	}
	if err := inst.Start(); err != nil {
		_ = inst.Close()
		return false, fmt.Errorf("start core instance for node %d: %w", e.opts.NodeID, err)
	}

	var sm stats.Manager
	if f := inst.GetFeature(stats.ManagerType()); f != nil {
		sm, _ = f.(stats.Manager)
	}

	userMap := make(map[int]panel.User, len(users))
	for _, u := range users {
		if u.ID > 0 && u.UUID != "" {
			userMap[u.ID] = u
		}
	}

	e.instance = inst
	e.statsMgr = sm
	e.currentCfg = cloneNodeConfig(nodeCfg)
	e.currentUsers = userMap
	return true, nil
}

// SyncUsers dynamically adds, removes, and updates users on the running MX inbound without restarting the listener.
// Returns (addedCount, removedCount, updatedCount, error).
func (e *NodeEngine) SyncUsers(users []panel.User) (int, int, int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.tracker.UpdateUsers(users)

	if e.instance == nil {
		newMap := make(map[int]panel.User, len(users))
		for _, u := range users {
			if u.ID > 0 && u.UUID != "" {
				newMap[u.ID] = u
			}
		}
		added := len(newMap)
		e.currentUsers = newMap
		return added, 0, 0, nil
	}

	um, err := e.getUserManagerLocked()
	if err != nil {
		return 0, 0, 0, err
	}

	newMap := make(map[int]panel.User, len(users))
	for _, u := range users {
		if u.ID > 0 && u.UUID != "" {
			newMap[u.ID] = u
		}
	}

	ctx := context.Background()
	var added, removed, updated int

	// 1. Remove deleted users or users whose UUID changed
	for uid, oldUser := range e.currentUsers {
		newUser, stillExists := newMap[uid]
		if !stillExists || newUser.UUID != oldUser.UUID {
			// Collect final traffic before removing
			e.collectUserTrafficLocked(uid)
			email := FormatUserEmail(e.opts.NodeID, uid)
			_ = um.RemoveUser(ctx, email)
			if !stillExists {
				removed++
			}
		}
	}

	// 2. Add new users or re-add users whose UUID changed
	for uid, newUser := range newMap {
		oldUser, existed := e.currentUsers[uid]
		if !existed || oldUser.UUID != newUser.UUID {
			acc, err := (&mx.Cuenta{Id: newUser.UUID}).AsAccount()
			if err != nil {
				return added, removed, updated, fmt.Errorf("create account for user %d: %w", uid, err)
			}
			memUser := &protocol.MemoryUser{
				Level:   0,
				Email:   FormatUserEmail(e.opts.NodeID, uid),
				Account: acc,
			}
			if err := um.AddUser(ctx, memUser); err != nil {
				return added, removed, updated, fmt.Errorf("add user %d: %w", uid, err)
			}
			if existed {
				updated++
			} else {
				added++
			}
		} else if oldUser.SpeedLimit != newUser.SpeedLimit || oldUser.DeviceLimit != newUser.DeviceLimit {
			updated++
		}
	}

	e.currentUsers = newMap
	return added, removed, updated, nil
}

// CollectAndResetTraffic atomically reads and resets per-user uplink/downlink byte counters.
func (e *NodeEngine) CollectAndResetTraffic() map[int][2]int64 {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.collectTrafficLocked()
	if len(e.pendingTraffic) == 0 {
		return nil
	}
	out := make(map[int][2]int64, len(e.pendingTraffic))
	for uid, pair := range e.pendingTraffic {
		if pair[0] > 0 || pair[1] > 0 {
			out[uid] = pair
		}
	}
	e.pendingTraffic = make(map[int][2]int64)
	return out
}

// RollbackTraffic restores traffic counts if pushing to the Xboard panel failed.
func (e *NodeEngine) RollbackTraffic(failed map[int][2]int64) {
	if len(failed) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for uid, pair := range failed {
		cur := e.pendingTraffic[uid]
		cur[0] += pair[0]
		cur[1] += pair[1]
		e.pendingTraffic[uid] = cur
	}
}

// Close stops the V2Ray core instance and cleans up tracker registration.
func (e *NodeEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	UnregisterTracker(e.tag)
	if e.instance != nil {
		err := e.instance.Close()
		e.instance = nil
		e.statsMgr = nil
		return err
	}
	return nil
}

func (e *NodeEngine) collectTrafficLocked() {
	if e.statsMgr == nil {
		return
	}
	for uid := range e.currentUsers {
		e.collectUserTrafficLocked(uid)
	}
}

func (e *NodeEngine) collectUserTrafficLocked(uid int) {
	if e.statsMgr == nil {
		return
	}
	email := FormatUserEmail(e.opts.NodeID, uid)
	upName := "user>>>" + email + ">>>traffic>>>uplink"
	downName := "user>>>" + email + ">>>traffic>>>downlink"

	var upDelta, downDelta int64
	if c := e.statsMgr.GetCounter(upName); c != nil {
		upDelta = c.Set(0)
	}
	if c := e.statsMgr.GetCounter(downName); c != nil {
		downDelta = c.Set(0)
	}
	if upDelta > 0 || downDelta > 0 {
		cur := e.pendingTraffic[uid]
		cur[0] += upDelta
		cur[1] += downDelta
		e.pendingTraffic[uid] = cur
	}
}

func (e *NodeEngine) getUserManagerLocked() (proxy.UserManager, error) {
	if e.instance == nil {
		return nil, fmt.Errorf("core instance not running")
	}
	imFeature := e.instance.GetFeature(inbound.ManagerType())
	if imFeature == nil {
		return nil, fmt.Errorf("inbound manager feature not found")
	}
	im, ok := imFeature.(inbound.Manager)
	if !ok {
		return nil, fmt.Errorf("invalid inbound manager type")
	}
	handler, err := im.GetHandler(context.Background(), e.tag)
	if err != nil {
		return nil, fmt.Errorf("get inbound handler %s: %w", e.tag, err)
	}
	gi, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("handler %s does not implement proxy.GetInbound", e.tag)
	}
	um, ok := gi.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("MX inbound does not implement proxy.UserManager")
	}
	return um, nil
}

func (e *NodeEngine) buildCoreConfig(nodeCfg *panel.NodeConfig, users []panel.User) (*core.Config, error) {
	transportext.RegisterAll()
	RegisterSkylineDispatcher()

	listenIP := strings.TrimSpace(e.opts.ListenIP)
	if listenIP == "" {
		listenIP = strings.TrimSpace(nodeCfg.ListenIP)
	}
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	parsedListenAddr := v2net.ParseAddress(listenIP)
	if parsedListenAddr == nil {
		return nil, fmt.Errorf("invalid listen_ip %q", listenIP)
	}
	if nodeCfg.ServerPort <= 0 || nodeCfg.ServerPort > 65535 {
		return nil, fmt.Errorf("invalid server_port %d", nodeCfg.ServerPort)
	}

	protoUsers := make([]*protocol.User, 0, len(users))
	for _, u := range users {
		if u.ID <= 0 || strings.TrimSpace(u.UUID) == "" {
			continue
		}
		protoUsers = append(protoUsers, &protocol.User{
			Level:   0,
			Email:   FormatUserEmail(e.opts.NodeID, u.ID),
			Account: serial.ToTypedMessage(&mx.Cuenta{Id: strings.TrimSpace(u.UUID)}),
		})
	}

	streamCfg, err := e.buildStreamConfig(nodeCfg)
	if err != nil {
		return nil, err
	}

	receiverCfg := &proxyman.ReceiverConfig{
		PortRange:      v2net.SinglePortRange(v2net.Port(nodeCfg.ServerPort)),
		Listen:         v2net.NewIPOrDomain(parsedListenAddr),
		StreamSettings: streamCfg,
	}

	mxServerCfg := &mx.ConfiguracionServidor{
		Users: protoUsers,
	}

	policyCfg := &policy.Config{
		Level: map[uint32]*policy.Policy{
			0: {
				Stats: &policy.Policy_Stats{
					UserUplink:   true,
					UserDownlink: true,
				},
			},
		},
		System: &policy.SystemPolicy{
			Stats: &policy.SystemPolicy_Stats{
				InboundUplink:    true,
				InboundDownlink:  true,
				OutboundUplink:   true,
				OutboundDownlink: true,
			},
		},
	}

	return &core.Config{
		App: []*anypb.Any{
			serial.ToTypedMessage(buildLogConfig(e.opts.LogLevel)),
			serial.ToTypedMessage(&wrapperspb.BoolValue{Value: true}), // SkylineDispatcher
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(policyCfg),
			serial.ToTypedMessage(&appstats.Config{}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag:              e.tag,
				ReceiverSettings: serial.ToTypedMessage(receiverCfg),
				ProxySettings:    serial.ToTypedMessage(mxServerCfg),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				Tag:           "direct",
				ProxySettings: serial.ToTypedMessage(&freedom.Config{}),
			},
			{
				Tag:           "block",
				ProxySettings: serial.ToTypedMessage(&blackhole.Config{}),
			},
		},
	}, nil
}

func (e *NodeEngine) buildStreamConfig(nodeCfg *panel.NodeConfig) (*internet.StreamConfig, error) {
	network := strings.ToLower(strings.TrimSpace(nodeCfg.Network))
	if network == "" {
		network = "tcp"
	}

	ns := nodeCfg.NetworkSettings
	streamCfg := &internet.StreamConfig{
		SocketSettings: &internet.SocketConfig{
			AcceptProxyProtocol: ns.AcceptProxyProtocol.Bool(),
		},
	}

	switch network {
	case "tcp":
		streamCfg.ProtocolName = "tcp"
		streamCfg.TransportSettings = []*internet.TransportConfig{
			{
				ProtocolName: "tcp",
				Settings: serial.ToTypedMessage(&tcp.Config{
					AcceptProxyProtocol: ns.AcceptProxyProtocol.Bool(),
				}),
			},
		}

	case "ws", "websocket":
		wsPath := ns.Path
		if wsPath == "" {
			wsPath = "/"
		}
		tc, err := transportext.EncodeTransportSettings(transportext.ExtendedTransportSettings{
			Network:             transportext.ProtocolWS,
			Path:                wsPath,
			Host:                ns.Host,
			Headers:             ns.Headers,
			AcceptProxyProtocol: ns.AcceptProxyProtocol.Bool(),
		})
		if err != nil {
			return nil, err
		}
		streamCfg.ProtocolName = transportext.ProtocolWS
		streamCfg.TransportSettings = []*internet.TransportConfig{tc}

	case "grpc", "gun":
		streamCfg.ProtocolName = "gun"
		streamCfg.TransportSettings = []*internet.TransportConfig{
			{
				ProtocolName: "gun",
				Settings: serial.ToTypedMessage(&grpc.Config{
					ServiceName: ns.ServiceName,
				}),
			},
		}

	case "xhttp", "jatp", "mc1", "mundordp":
		protoName := network
		if protoName == "jatp" {
			protoName = transportext.ProtocolXHTTP
		}
		tc, err := transportext.EncodeTransportSettings(transportext.ExtendedTransportSettings{
			Network:                protoName,
			Path:                   ns.Path,
			Host:                   ns.Host,
			Headers:                ns.Headers,
			Mode:                   ns.Mode,
			Username:               ns.Username,
			CertificateFingerprint: ns.CertificateFingerprint,
			FakeTitle:              ns.FakeTitle,
			FakeMessage:            ns.FakeMessage,
			AcceptProxyProtocol:    ns.AcceptProxyProtocol.Bool(),
			UseTLSCertificate:      ns.UseTLSCertificate.Bool(),
			CIDRSegments:           []string(ns.CIDRSegments),
		})
		if err != nil {
			return nil, err
		}
		streamCfg.ProtocolName = protoName
		streamCfg.TransportSettings = []*internet.TransportConfig{tc}

	default:
		return nil, fmt.Errorf("unsupported MX transport network %q", network)
	}

	enableTLS := nodeCfg.TLS == 1 || nodeCfg.TLS == 2 || (network == "mundordp" && ns.UseTLSCertificate.Bool())
	if enableTLS {
		if nodeCfg.TLS == 2 {
			if _, err := transportext.BuildRealityConfig(
				nodeCfg.TLSSettings.ServerName,
				nodeCfg.TLSSettings.PrivateKey,
				nodeCfg.TLSSettings.PublicKey,
				nodeCfg.TLSSettings.ShortID,
				nodeCfg.TLSSettings.ServerPort,
			); err != nil {
				return nil, err
			}
		}

		certFile := e.opts.CertFile
		if certFile == "" {
			certFile = nodeCfg.CertConfig.CertFile
		}
		if certFile == "" {
			certFile = nodeCfg.TLSSettings.CertFile
		}
		keyFile := e.opts.KeyFile
		if keyFile == "" {
			keyFile = nodeCfg.CertConfig.KeyFile
		}
		if keyFile == "" {
			keyFile = nodeCfg.TLSSettings.KeyFile
		}

		serverName := nodeCfg.TLSSettings.ServerName
		if serverName == "" {
			serverName = nodeCfg.ServerName
		}
		if serverName == "" {
			serverName = nodeCfg.CertConfig.CertDomain
		}

		v2TLS, err := transportext.BuildV2TLSConfig(
			serverName,
			nodeCfg.Host,
			certFile,
			keyFile,
			nodeCfg.CertConfig.CertPem,
			nodeCfg.CertConfig.KeyPem,
			nodeCfg.TLSSettings.AllowInsecure.Bool(),
		)
		if err != nil {
			return nil, err
		}

		streamCfg.SecurityType = serial.GetMessageType(v2TLS)
		streamCfg.SecuritySettings = []*anypb.Any{serial.ToTypedMessage(v2TLS)}
	}

	return streamCfg, nil
}

func needsCoreRestart(oldCfg, newCfg *panel.NodeConfig) bool {
	if oldCfg == nil || newCfg == nil {
		return true
	}
	if oldCfg.ListenIP != newCfg.ListenIP ||
		oldCfg.ServerPort != newCfg.ServerPort ||
		oldCfg.Network != newCfg.Network ||
		oldCfg.TLS != newCfg.TLS ||
		oldCfg.Host != newCfg.Host ||
		oldCfg.ServerName != newCfg.ServerName {
		return true
	}
	if !reflect.DeepEqual(oldCfg.NetworkSettings, newCfg.NetworkSettings) {
		return true
	}
	if !reflect.DeepEqual(oldCfg.TLSSettings, newCfg.TLSSettings) {
		return true
	}
	if !reflect.DeepEqual(oldCfg.CertConfig, newCfg.CertConfig) {
		return true
	}
	return false
}

func buildLogConfig(level string) *applog.Config {
	errType := applog.LogType_Console
	sev := commlog.Severity_Warning
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		sev = commlog.Severity_Debug
	case "info":
		sev = commlog.Severity_Info
	case "warn", "warning":
		sev = commlog.Severity_Warning
	case "error":
		sev = commlog.Severity_Error
	case "none", "off":
		errType = applog.LogType_None
		sev = commlog.Severity_Unknown
	}
	return &applog.Config{
		Error:  &applog.LogSpecification{Type: errType, Level: sev},
		Access: &applog.LogSpecification{Type: applog.LogType_None},
	}
}

func cloneNodeConfig(cfg *panel.NodeConfig) *panel.NodeConfig {
	if cfg == nil {
		return nil
	}
	cp := *cfg
	return &cp
}
