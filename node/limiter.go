package node

import (
	"fmt"
	"net"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v2fly/v2ray-core/v5/common"
	"github.com/v2fly/v2ray-core/v5/common/buf"
	v2net "github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/transport"

	"skyline/panel"
)

// TokenBucket implements a thread-safe token-bucket rate limiter in bytes/sec.
type TokenBucket struct {
	mu         sync.Mutex
	rate       float64 // bytes per second
	burst      float64
	tokens     float64
	lastRefill time.Time
}

// NewTokenBucket creates a token bucket for the given rate in bytes/sec.
func NewTokenBucket(bytesPerSec int64) *TokenBucket {
	now := time.Now()
	r := float64(bytesPerSec)
	burst := max(r*0.5, 64*1024)
	return &TokenBucket{
		rate:       r,
		burst:      burst,
		tokens:     burst,
		lastRefill: now,
	}
}

// SetRate updates the token bucket rate dynamically without resetting active connections.
func (tb *TokenBucket) SetRate(bytesPerSec int64) {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	r := float64(bytesPerSec)
	tb.rate = r
	tb.burst = max(r*0.5, 64*1024)
	if tb.tokens > tb.burst {
		tb.tokens = tb.burst
	}
}

// Wait blocks until n bytes are permitted by the token bucket.
func (tb *TokenBucket) Wait(n int) {
	if n <= 0 {
		return
	}
	tb.mu.Lock()
	if tb.rate <= 0 {
		tb.mu.Unlock()
		return
	}
	now := time.Now()
	if elapsed := now.Sub(tb.lastRefill).Seconds(); elapsed > 0 {
		tb.tokens = min(tb.burst, tb.tokens+elapsed*tb.rate)
		tb.lastRefill = now
	}
	tb.tokens -= float64(n)
	var waitDur time.Duration
	if tb.tokens < 0 {
		waitDur = min(time.Duration((-tb.tokens)/tb.rate*float64(time.Second)), 2*time.Second)
	}
	tb.mu.Unlock()

	if waitDur > 0 {
		time.Sleep(waitDur)
	}
}

// UserPolicyState holds runtime policy and rate limiters for a single user on a node.
type UserPolicyState struct {
	UserID         int
	UUID           string
	Email          string
	SpeedLimitMbps int
	DeviceLimit    int
	UpLimiter      *TokenBucket
	DownLimiter    *TokenBucket
}

type ipEntry struct {
	activeConns int
	lastSeen    time.Time
}

// NodeTracker tracks per-user active IPs, connections, rate limits, and routing rules for a node.
type NodeTracker struct {
	NodeID    int
	Tag       string
	DeviceTTL time.Duration
	StartedAt time.Time

	mu                sync.RWMutex
	usersByEmail      map[string]*UserPolicyState
	usersByID         map[int]*UserPolicyState
	activeIPs         map[int]map[string]*ipEntry
	userActiveConns   map[int]int
	remoteAliveCounts map[int]int
	lastPushedCounts  map[int]int
	routes            []panel.RouteRule

	activeConns atomic.Int64
	totalConns  atomic.Int64
}

var (
	trackerRegistryMu sync.RWMutex
	trackerRegistry   = make(map[string]*NodeTracker)
)

// RegisterTracker registers a NodeTracker by its inbound tag.
func RegisterTracker(tag string, tracker *NodeTracker) {
	trackerRegistryMu.Lock()
	trackerRegistry[tag] = tracker
	trackerRegistryMu.Unlock()
}

// UnregisterTracker removes a NodeTracker by its inbound tag.
func UnregisterTracker(tag string) {
	trackerRegistryMu.Lock()
	delete(trackerRegistry, tag)
	trackerRegistryMu.Unlock()
}

// GetTracker retrieves a registered NodeTracker by its inbound tag.
func GetTracker(tag string) *NodeTracker {
	trackerRegistryMu.RLock()
	t := trackerRegistry[tag]
	trackerRegistryMu.RUnlock()
	return t
}

// NewNodeTracker creates a new NodeTracker for a given node.
func NewNodeTracker(nodeID int, tag string, deviceTTL time.Duration) *NodeTracker {
	if deviceTTL <= 0 {
		deviceTTL = 90 * time.Second
	}
	return &NodeTracker{
		NodeID:            nodeID,
		Tag:               tag,
		DeviceTTL:         deviceTTL,
		StartedAt:         time.Now(),
		usersByEmail:      make(map[string]*UserPolicyState),
		usersByID:         make(map[int]*UserPolicyState),
		activeIPs:         make(map[int]map[string]*ipEntry),
		userActiveConns:   make(map[int]int),
		remoteAliveCounts: make(map[int]int),
		lastPushedCounts:  make(map[int]int),
	}
}

// FormatUserEmail returns the canonical V2Ray stats email for a user on a node.
func FormatUserEmail(nodeID, userID int) string {
	return fmt.Sprintf("mx-node-%d-uid-%d", nodeID, userID)
}

// UpdateUsers updates the user policy table (speed_limit, device_limit) in-place.
func (nt *NodeTracker) UpdateUsers(users []panel.User) {
	nt.mu.Lock()
	defer nt.mu.Unlock()

	newByEmail := make(map[string]*UserPolicyState, len(users))
	newByID := make(map[int]*UserPolicyState, len(users))

	for _, u := range users {
		email := FormatUserEmail(nt.NodeID, u.ID)
		state, exists := nt.usersByID[u.ID]
		bytesPerSec := int64(u.SpeedLimit) * 125000 // 1 Mbps = 125,000 B/s
		if !exists {
			state = &UserPolicyState{
				UserID:         u.ID,
				UUID:           u.UUID,
				Email:          email,
				SpeedLimitMbps: u.SpeedLimit,
				DeviceLimit:    u.DeviceLimit,
			}
			if bytesPerSec > 0 {
				state.UpLimiter = NewTokenBucket(bytesPerSec)
				state.DownLimiter = NewTokenBucket(bytesPerSec)
			}
		} else {
			state.UUID = u.UUID
			state.Email = email
			state.SpeedLimitMbps = u.SpeedLimit
			state.DeviceLimit = u.DeviceLimit
			if bytesPerSec > 0 {
				if state.UpLimiter == nil {
					state.UpLimiter = NewTokenBucket(bytesPerSec)
					state.DownLimiter = NewTokenBucket(bytesPerSec)
				} else {
					state.UpLimiter.SetRate(bytesPerSec)
					state.DownLimiter.SetRate(bytesPerSec)
				}
			} else {
				state.UpLimiter = nil
				state.DownLimiter = nil
			}
		}
		newByEmail[email] = state
		newByID[u.ID] = state
	}

	for uid := range nt.activeIPs {
		if _, ok := newByID[uid]; !ok {
			delete(nt.activeIPs, uid)
			delete(nt.userActiveConns, uid)
		}
	}

	nt.usersByEmail = newByEmail
	nt.usersByID = newByID
}

// UpdateRemoteAlive updates the remote device counts from GET alivelist.
func (nt *NodeTracker) UpdateRemoteAlive(alive map[int]int) {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	nt.remoteAliveCounts = make(map[int]int, len(alive))
	for k, v := range alive {
		nt.remoteAliveCounts[k] = v
	}
}

// SetRoutes updates the Xboard route rules for this node.
func (nt *NodeTracker) SetRoutes(routes []panel.RouteRule) {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	nt.routes = append([]panel.RouteRule(nil), routes...)
}

// MatchRoute evaluates configured Xboard routes against the target destination.
func (nt *NodeTracker) MatchRoute(dest v2net.Destination, protocol string) (string, string) {
	nt.mu.RLock()
	routes := nt.routes
	nt.mu.RUnlock()
	if len(routes) == 0 {
		return "", ""
	}

	targetStr := strings.ToLower(dest.Address.String())
	targetNet := strings.ToLower(dest.Network.SystemString())
	var targetIP net.IP
	if dest.Address.Family().IsIP() {
		targetIP = dest.Address.IP()
	}

	for _, rule := range routes {
		for _, pattern := range rule.Match {
			p := strings.ToLower(strings.TrimSpace(pattern))
			if p != "" && matchSinglePattern(p, targetStr, targetIP, targetNet, strings.ToLower(protocol)) {
				return strings.ToLower(strings.TrimSpace(rule.Action)), strings.TrimSpace(rule.ActionValue)
			}
		}
	}
	return "", ""
}

func matchSinglePattern(pattern, hostStr string, targetIP net.IP, network, protoName string) bool {
	if after, ok := strings.CutPrefix(pattern, "protocol:"); ok {
		return after != "" && (protoName == after || network == after)
	}
	if after, ok := strings.CutPrefix(pattern, "full:"); ok {
		return hostStr == after
	}
	if after, ok := strings.CutPrefix(pattern, "keyword:"); ok {
		return strings.Contains(hostStr, after)
	}
	pattern = strings.TrimPrefix(pattern, "domain:")

	if strings.Contains(pattern, "/") && targetIP != nil {
		if _, cidr, err := net.ParseCIDR(pattern); err == nil && cidr.Contains(targetIP) {
			return true
		}
	}
	if hostStr == pattern {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		if strings.HasSuffix(hostStr, pattern[1:]) || hostStr == pattern[2:] {
			return true
		}
	}
	if matched, err := path.Match(pattern, hostStr); err == nil && matched {
		return true
	}
	return strings.HasSuffix(hostStr, "."+pattern)
}

// AcquireConnection validates device_limit and records an active connection for (email, clientIP).
func (nt *NodeTracker) AcquireConnection(email, clientIP string) (*UserPolicyState, func(), error) {
	now := time.Now()
	clientIP = normalizeClientIP(clientIP)

	nt.mu.Lock()
	state, ok := nt.usersByEmail[email]
	if !ok {
		nt.mu.Unlock()
		nt.activeConns.Add(1)
		nt.totalConns.Add(1)
		var once sync.Once
		return nil, func() {
			once.Do(func() { nt.activeConns.Add(-1) })
		}, nil
	}

	uid := state.UserID
	ipMap := nt.activeIPs[uid]
	if ipMap == nil {
		ipMap = make(map[string]*ipEntry)
		nt.activeIPs[uid] = ipMap
	}

	for ip, entry := range ipMap {
		if entry.activeConns <= 0 && now.Sub(entry.lastSeen) > nt.DeviceTTL {
			delete(ipMap, ip)
		}
	}

	existingEntry := ipMap[clientIP]
	if state.DeviceLimit > 0 && clientIP != "" && existingEntry == nil {
		remoteOther := max(0, nt.remoteAliveCounts[uid]-nt.lastPushedCounts[uid])
		if remoteOther+len(ipMap)+1 > state.DeviceLimit {
			nt.mu.Unlock()
			return nil, nil, fmt.Errorf("device limit (%d) exceeded for user %d from IP %s", state.DeviceLimit, uid, clientIP)
		}
	}

	if clientIP != "" {
		if existingEntry == nil {
			existingEntry = &ipEntry{}
			ipMap[clientIP] = existingEntry
		}
		existingEntry.activeConns++
		existingEntry.lastSeen = now
	}
	nt.userActiveConns[uid]++
	nt.mu.Unlock()

	nt.activeConns.Add(1)
	nt.totalConns.Add(1)

	var releaseOnce sync.Once
	releaseFn := func() {
		releaseOnce.Do(func() {
			nt.activeConns.Add(-1)
			nt.mu.Lock()
			if nt.userActiveConns[uid] > 0 {
				nt.userActiveConns[uid]--
			}
			if clientIP != "" {
				if m := nt.activeIPs[uid]; m != nil {
					if e := m[clientIP]; e != nil {
						e.activeConns--
						e.lastSeen = time.Now()
					}
				}
			}
			nt.mu.Unlock()
		})
	}

	return state, releaseFn, nil
}

// SnapshotAliveIPs returns a map of userID -> active client IPs for reporting to POST alive.
func (nt *NodeTracker) SnapshotAliveIPs() map[int][]string {
	now := time.Now()
	nt.mu.Lock()
	defer nt.mu.Unlock()

	out := make(map[int][]string)
	pushedCounts := make(map[int]int)
	for uid, ipMap := range nt.activeIPs {
		var ips []string
		for ip, entry := range ipMap {
			if entry.activeConns > 0 || now.Sub(entry.lastSeen) <= nt.DeviceTTL {
				ips = append(ips, ip)
			} else {
				delete(ipMap, ip)
			}
		}
		if len(ips) > 0 {
			out[uid] = ips
			pushedCounts[uid] = len(ips)
		}
	}
	nt.lastPushedCounts = pushedCounts
	return out
}

// SnapshotOnlineConns returns per-user active connection counts and node-level metrics.
func (nt *NodeTracker) SnapshotOnlineConns() (map[int]int, int64, int64, int, int) {
	nt.mu.RLock()
	defer nt.mu.RUnlock()

	online := make(map[int]int)
	for uid, count := range nt.userActiveConns {
		if count > 0 {
			online[uid] = count
		}
	}
	return online, nt.activeConns.Load(), nt.totalConns.Load(), len(nt.usersByID), len(online)
}

func normalizeClientIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	return strings.Trim(raw, "[]")
}

// WrapLinkWithTracking wraps a transport.Link with connection lifecycle tracking and optional token-bucket rate limiting.
func WrapLinkWithTracking(link *transport.Link, state *UserPolicyState, releaseFn func()) *transport.Link {
	var upLimiter, downLimiter *TokenBucket
	if state != nil {
		upLimiter = state.UpLimiter
		downLimiter = state.DownLimiter
	}
	return &transport.Link{
		Reader: &trackedReader{
			reader:    link.Reader,
			limiter:   downLimiter,
			releaseFn: releaseFn,
		},
		Writer: &trackedWriter{
			writer:    link.Writer,
			limiter:   upLimiter,
			releaseFn: releaseFn,
		},
	}
}

type trackedReader struct {
	reader    buf.Reader
	limiter   *TokenBucket
	releaseFn func()
}

func (r *trackedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	mb, err := r.reader.ReadMultiBuffer()
	if r.limiter != nil && !mb.IsEmpty() {
		r.limiter.Wait(int(mb.Len()))
	}
	if err != nil && r.releaseFn != nil {
		r.releaseFn()
	}
	return mb, err
}

func (r *trackedReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	if tr, ok := r.reader.(buf.TimeoutReader); ok {
		mb, err := tr.ReadMultiBufferTimeout(timeout)
		if r.limiter != nil && !mb.IsEmpty() {
			r.limiter.Wait(int(mb.Len()))
		}
		if err != nil && r.releaseFn != nil {
			r.releaseFn()
		}
		return mb, err
	}
	return r.ReadMultiBuffer()
}

func (r *trackedReader) Interrupt() {
	if r.releaseFn != nil {
		r.releaseFn()
	}
	common.Interrupt(r.reader)
}

func (r *trackedReader) Close() error {
	if r.releaseFn != nil {
		r.releaseFn()
	}
	return common.Close(r.reader)
}

type trackedWriter struct {
	writer    buf.Writer
	limiter   *TokenBucket
	releaseFn func()
}

func (w *trackedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if w.limiter != nil && !mb.IsEmpty() {
		w.limiter.Wait(int(mb.Len()))
	}
	err := w.writer.WriteMultiBuffer(mb)
	if err != nil && w.releaseFn != nil {
		w.releaseFn()
	}
	return err
}

func (w *trackedWriter) Close() error {
	if w.releaseFn != nil {
		w.releaseFn()
	}
	return common.Close(w.writer)
}
