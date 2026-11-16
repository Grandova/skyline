package node

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"skyline/config"
	"skyline/panel"
)

// NodeWorker manages the full synchronization and reporting lifecycle for a single Xboard MX node.
type NodeWorker struct {
	targetCfg config.NodeTargetConfig
	client    *panel.Client
	engine    *NodeEngine
	logger    *log.Logger

	mu           sync.Mutex
	pullInterval time.Duration
	pushInterval time.Duration
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// NewNodeWorker creates a new NodeWorker for the given target configuration.
func NewNodeWorker(target config.NodeTargetConfig, logger *log.Logger) *NodeWorker {
	if logger == nil {
		logger = log.Default()
	}
	client := panel.NewClient(panel.ClientOptions{
		PanelURL:   target.PanelURL,
		Token:      target.Token,
		NodeID:     target.NodeID,
		NodeType:   target.NodeType,
		MachineID:  target.MachineID,
		APIVersion: target.APIVersion,
		Timeout:    target.HTTPTimeout,
	})
	engine := NewNodeEngine(EngineOptions{
		NodeID:              target.NodeID,
		LogLevel:            target.LogLevel,
		ListenIP:            target.ListenIP,
		CertFile:            target.CertFile,
		KeyFile:             target.KeyFile,
		ProxyProtocol:       target.ProxyProtocol.Int(),
		AcceptProxyProtocol: target.AcceptProxyProtocol,
		ForwardAddress:      target.ForwardAddress,
		DeviceTTL:           target.DeviceTTL,
	})
	pullInt := target.PullInterval
	if pullInt <= 0 {
		pullInt = 60 * time.Second
	}
	pushInt := target.PushInterval
	if pushInt <= 0 {
		pushInt = 60 * time.Second
	}
	return &NodeWorker{
		targetCfg:    target,
		client:       client,
		engine:       engine,
		logger:       logger,
		pullInterval: pullInt,
		pushInterval: pushInt,
	}
}

// Engine returns the underlying NodeEngine.
func (w *NodeWorker) Engine() *NodeEngine {
	return w.engine
}

// Client returns the underlying panel.Client.
func (w *NodeWorker) Client() *panel.Client {
	return w.client
}

// SyncOnce executes a single pull cycle (config, users, alivelist) and applies any changes.
func (w *NodeWorker) SyncOnce(ctx context.Context) error {
	nodeCfg, cfgModified, err := w.client.GetNodeConfig(ctx)
	if err != nil {
		return fmt.Errorf("fetch node config: %w", err)
	}
	if nodeCfg == nil {
		return fmt.Errorf("panel returned empty config for node %d", w.targetCfg.NodeID)
	}

	// Update intervals from panel base_config if not overridden in local config
	w.mu.Lock()
	if w.targetCfg.PullInterval <= 0 && nodeCfg.BaseConfig.PullInterval > 0 {
		w.pullInterval = time.Duration(nodeCfg.BaseConfig.PullInterval) * time.Second
	}
	if w.targetCfg.PushInterval <= 0 && nodeCfg.BaseConfig.PushInterval > 0 {
		w.pushInterval = time.Duration(nodeCfg.BaseConfig.PushInterval) * time.Second
	}
	w.mu.Unlock()

	users, usersModified, err := w.client.GetUsers(ctx)
	if err != nil {
		return fmt.Errorf("fetch users: %w", err)
	}

	if !w.engine.IsRunning() || cfgModified {
		restarted, err := w.engine.ApplyConfig(nodeCfg, users)
		if err != nil {
			return err
		}
		if restarted {
			w.logger.Printf("[Node #%d] MX server listening on %s:%d (network=%s, tls=%d, users=%d)",
				w.targetCfg.NodeID, nodeCfg.ListenIP, nodeCfg.ServerPort, nodeCfg.Network, nodeCfg.TLS, len(users))
		} else if usersModified {
			added, removed, updated, err := w.engine.SyncUsers(users)
			if err != nil {
				return err
			}
			if added > 0 || removed > 0 || updated > 0 {
				w.logger.Printf("[Node #%d] Hot-synced users: +%d -%d ~%d (total=%d)",
					w.targetCfg.NodeID, added, removed, updated, len(users))
			}
		}
	} else if usersModified {
		added, removed, updated, err := w.engine.SyncUsers(users)
		if err != nil {
			return err
		}
		if added > 0 || removed > 0 || updated > 0 {
			w.logger.Printf("[Node #%d] Hot-synced users: +%d -%d ~%d (total=%d)",
				w.targetCfg.NodeID, added, removed, updated, len(users))
		}
	}

	if aliveList, err := w.client.GetAliveList(ctx); err == nil && aliveList != nil {
		w.engine.Tracker().UpdateRemoteAlive(aliveList)
	}

	return nil
}

// ReportOnce executes a single push cycle (traffic, alive IPs, node status).
func (w *NodeWorker) ReportOnce(ctx context.Context) error {
	traffic := w.engine.CollectAndResetTraffic()
	alive := w.engine.Tracker().SnapshotAliveIPs()
	status := CollectNodeStatus()

	if w.targetCfg.APIVersion == "v2" {
		online, activeConns, totalConns, totalUsers, activeUsers := w.engine.Tracker().SnapshotOnlineConns()
		report := panel.NodeReport{
			Status: &status,
			Metrics: map[string]any{
				"uptime":             int64(time.Since(w.engine.Tracker().StartedAt).Seconds()),
				"goroutines":         runtime.NumGoroutine(),
				"active_connections": activeConns,
				"total_connections":  totalConns,
				"total_users":        totalUsers,
				"active_users":       activeUsers,
				"kernel_status":      w.engine.IsRunning(),
			},
		}
		if len(traffic) > 0 {
			report.Traffic = make(map[string][2]int64, len(traffic))
			for uid, pair := range traffic {
				report.Traffic[strconv.Itoa(uid)] = pair
			}
		}
		if len(alive) > 0 {
			report.Alive = make(map[string][]string, len(alive))
			for uid, ips := range alive {
				report.Alive[strconv.Itoa(uid)] = ips
			}
		}
		if len(online) > 0 {
			report.Online = make(map[string]int, len(online))
			for uid, c := range online {
				report.Online[strconv.Itoa(uid)] = c
			}
		}
		if err := w.client.PushReport(ctx, report); err != nil {
			w.engine.RollbackTraffic(traffic)
			return fmt.Errorf("push v2 report: %w", err)
		}
		return nil
	}

	var firstErr error
	if len(traffic) > 0 {
		if err := w.client.PushTraffic(ctx, traffic); err != nil {
			w.engine.RollbackTraffic(traffic)
			firstErr = fmt.Errorf("push traffic: %w", err)
		}
	}
	if len(alive) > 0 {
		if err := w.client.PushAlive(ctx, alive); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("push alive: %w", err)
		}
	}
	if err := w.client.PushStatus(ctx, status); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("push status: %w", err)
	}
	return firstErr
}

// Start performs an initial sync and launches background pull/push goroutines.
func (w *NodeWorker) Start(parentCtx context.Context) error {
	ctx, cancel := context.WithCancel(parentCtx)
	w.mu.Lock()
	w.cancel = cancel
	w.mu.Unlock()

	if err := w.SyncOnce(ctx); err != nil {
		w.logger.Printf("[Node #%d] Initial sync warning (will retry in background): %v", w.targetCfg.NodeID, err)
	} else {
		_ = w.ReportOnce(ctx)
	}

	w.wg.Add(2)
	go w.pullLoop(ctx)
	go w.pushLoop(ctx)
	return nil
}

func (w *NodeWorker) pullLoop(ctx context.Context) {
	defer w.wg.Done()
	for {
		w.mu.Lock()
		interval := w.pullInterval
		w.mu.Unlock()
		if interval < time.Second {
			interval = 10 * time.Second
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := w.SyncOnce(ctx); err != nil {
				w.logger.Printf("[Node #%d] Pull sync error: %v", w.targetCfg.NodeID, err)
			}
		}
	}
}

func (w *NodeWorker) pushLoop(ctx context.Context) {
	defer w.wg.Done()
	for {
		w.mu.Lock()
		interval := w.pushInterval
		w.mu.Unlock()
		if interval < time.Second {
			interval = 10 * time.Second
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := w.ReportOnce(ctx); err != nil {
				w.logger.Printf("[Node #%d] Push report error: %v", w.targetCfg.NodeID, err)
			}
		}
	}
}

// Stop flushes final traffic and shuts down the NodeWorker and its V2Ray core instance.
func (w *NodeWorker) Stop() error {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	w.wg.Wait()

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	if traffic := w.engine.CollectAndResetTraffic(); len(traffic) > 0 {
		if w.targetCfg.APIVersion == "v2" {
			report := panel.NodeReport{
				Traffic: make(map[string][2]int64, len(traffic)),
			}
			for uid, pair := range traffic {
				report.Traffic[strconv.Itoa(uid)] = pair
			}
			_ = w.client.PushReport(flushCtx, report)
		} else {
			_ = w.client.PushTraffic(flushCtx, traffic)
		}
	}

	return w.engine.Close()
}

func targetNeedsWorkerRecreate(oldT, newT config.NodeTargetConfig) bool {
	oldAccept := false
	if oldT.AcceptProxyProtocol != nil {
		oldAccept = *oldT.AcceptProxyProtocol
	}
	newAccept := false
	if newT.AcceptProxyProtocol != nil {
		newAccept = *newT.AcceptProxyProtocol
	}
	return oldT.PanelURL != newT.PanelURL ||
		oldT.Token != newT.Token ||
		oldT.NodeID != newT.NodeID ||
		oldT.NodeType != newT.NodeType ||
		oldT.MachineID != newT.MachineID ||
		oldT.APIVersion != newT.APIVersion ||
		oldT.ListenIP != newT.ListenIP ||
		oldT.CertFile != newT.CertFile ||
		oldT.KeyFile != newT.KeyFile ||
		oldT.ProxyProtocol != newT.ProxyProtocol ||
		oldAccept != newAccept ||
		oldT.ForwardAddress != newT.ForwardAddress
}

// Manager coordinates all configured NodeWorkers (including Xboard V2 Machine mode).
type Manager struct {
	cfg     *config.Config
	logger  *log.Logger
	mu      sync.Mutex
	workers map[int]*NodeWorker
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewManager creates a new Skyline node Manager.
func NewManager(cfg *config.Config, logger *log.Logger) *Manager {
	if logger == nil {
		logger = log.Default()
	}
	return &Manager{
		cfg:     cfg,
		logger:  logger,
		workers: make(map[int]*NodeWorker),
	}
}

// Start launches all configured node workers or starts machine node discovery.
func (m *Manager) Start(parentCtx context.Context) error {
	if err := m.cfg.Validate(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(parentCtx)
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	if len(m.cfg.Nodes) > 0 {
		for _, target := range m.cfg.Nodes {
			w := NewNodeWorker(target, m.logger)
			m.mu.Lock()
			m.workers[target.NodeID] = w
			m.mu.Unlock()
			if err := w.Start(ctx); err != nil {
				return err
			}
		}
		return nil
	}

	// Machine mode: discover MX nodes bound to machine_id
	if m.cfg.MachineID > 0 {
		if err := m.syncMachineNodes(ctx); err != nil {
			m.logger.Printf("[Machine #%d] Initial node discovery warning: %v", m.cfg.MachineID, err)
		}
		m.wg.Add(1)
		go m.machineLoop(ctx)
	}
	return nil
}

func (m *Manager) syncMachineNodes(ctx context.Context) error {
	mc := panel.NewClient(panel.ClientOptions{
		PanelURL:   m.cfg.PanelURL,
		Token:      m.cfg.Token,
		MachineID:  m.cfg.MachineID,
		APIVersion: "v2",
		Timeout:    m.cfg.HTTPTimeout,
	})
	resp, err := mc.GetMachineNodes(ctx)
	if err != nil {
		return err
	}
	_ = mc.PushMachineStatus(ctx, CollectNodeStatus())

	desired := make(map[int]panel.MachineNodeInfo)
	for _, n := range resp.Nodes {
		if strings.EqualFold(n.Type, "mx") {
			desired[n.ID] = n
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, w := range m.workers {
		if _, ok := desired[id]; !ok {
			m.logger.Printf("[Machine #%d] Stopping removed MX node #%d", m.cfg.MachineID, id)
			_ = w.Stop()
			delete(m.workers, id)
		}
	}

	for id := range desired {
		if _, exists := m.workers[id]; !exists {
			target := config.NodeTargetConfig{
				PanelURL:            m.cfg.PanelURL,
				Token:               m.cfg.Token,
				NodeID:              id,
				NodeType:            "mx",
				MachineID:           m.cfg.MachineID,
				APIVersion:          m.cfg.APIVersion,
				ListenIP:            m.cfg.ListenIP,
				CertFile:            m.cfg.CertFile,
				KeyFile:             m.cfg.KeyFile,
				ProxyProtocol:       m.cfg.ProxyProtocol,
				AcceptProxyProtocol: m.cfg.AcceptProxyProtocol,
				ForwardAddress:      m.cfg.ForwardAddress,
				PullInterval:        m.cfg.PullInterval,
				PushInterval:        m.cfg.PushInterval,
				HTTPTimeout:         m.cfg.HTTPTimeout,
				DeviceTTL:           m.cfg.DeviceTTL,
			}
			w := NewNodeWorker(target, m.logger)
			m.workers[id] = w
			_ = w.Start(ctx)
		}
	}
	return nil
}

func (m *Manager) machineLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.syncMachineNodes(ctx); err != nil {
				m.logger.Printf("[Machine #%d] Sync error: %v", m.cfg.MachineID, err)
			}
		}
	}
}

// Reload hot-reloads the Manager with a new configuration without dropping unchanged node workers.
func (m *Manager) Reload(parentCtx context.Context, newCfg *config.Config) error {
	if err := newCfg.Validate(); err != nil {
		return err
	}

	// If switching to/from machine mode, perform full restart
	if (m.cfg.MachineID > 0 && len(m.cfg.Nodes) == 0) != (newCfg.MachineID > 0 && len(newCfg.Nodes) == 0) {
		_ = m.Stop()
		m.cfg = newCfg
		return m.Start(parentCtx)
	}

	if newCfg.MachineID > 0 && len(newCfg.Nodes) == 0 {
		m.mu.Lock()
		m.cfg = newCfg
		m.mu.Unlock()
		return m.syncMachineNodes(parentCtx)
	}

	desired := make(map[int]config.NodeTargetConfig, len(newCfg.Nodes))
	for _, t := range newCfg.Nodes {
		desired[t.NodeID] = t
	}

	m.mu.Lock()
	var toStop []*NodeWorker
	for id, w := range m.workers {
		newTarget, stillWanted := desired[id]
		if !stillWanted || targetNeedsWorkerRecreate(w.targetCfg, newTarget) {
			toStop = append(toStop, w)
			delete(m.workers, id)
		} else {
			w.mu.Lock()
			w.targetCfg = newTarget
			if newTarget.PullInterval > 0 {
				w.pullInterval = newTarget.PullInterval
			}
			if newTarget.PushInterval > 0 {
				w.pushInterval = newTarget.PushInterval
			}
			w.mu.Unlock()
			w.client.ResetETags()
		}
	}
	m.cfg = newCfg
	m.mu.Unlock()

	for _, w := range toStop {
		_ = w.Stop()
	}

	for id, target := range desired {
		m.mu.Lock()
		_, exists := m.workers[id]
		m.mu.Unlock()
		if !exists {
			w := NewNodeWorker(target, m.logger)
			m.mu.Lock()
			m.workers[id] = w
			m.mu.Unlock()
			if err := w.Start(parentCtx); err != nil {
				return err
			}
		}
	}
	return nil
}

// Stop gracefully stops all running NodeWorkers.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	workers := make([]*NodeWorker, 0, len(m.workers))
	for _, w := range m.workers {
		workers = append(workers, w)
	}
	m.workers = make(map[int]*NodeWorker)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()

	var firstErr error
	for _, w := range workers {
		if err := w.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
