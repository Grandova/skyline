package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FlexibleBool unmarshals from JSON boolean, number (0/1), or string ("true"/"false"/"1"/"0").
type FlexibleBool bool

func (b *FlexibleBool) UnmarshalJSON(data []byte) error {
	switch strings.ToLower(strings.TrimSpace(string(data))) {
	case "true", "1", `"true"`, `"1"`:
		*b = true
		return nil
	case "false", "0", "null", `""`, `"false"`, `"0"`:
		*b = false
		return nil
	default:
		var v bool
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*b = FlexibleBool(v)
		return nil
	}
}

func (b FlexibleBool) Bool() bool {
	return bool(b)
}

// StringSlice unmarshals from either a JSON array of strings or a comma-separated string.
type StringSlice []string

func (s *StringSlice) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		var arr []string
		if err := json.Unmarshal(trimmed, &arr); err == nil {
			*s = arr
			return nil
		}
		var anyArr []any
		if err := json.Unmarshal(trimmed, &anyArr); err != nil {
			return err
		}
		out := make([]string, 0, len(anyArr))
		for _, item := range anyArr {
			out = append(out, fmt.Sprint(item))
		}
		*s = out
		return nil
	}
	var single string
	if err := json.Unmarshal(trimmed, &single); err != nil {
		return err
	}
	if single == "" {
		*s = nil
		return nil
	}
	parts := strings.Split(single, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	*s = out
	return nil
}

func isEmptyJSONArrayOrNull(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("[]"))
}

// FlexibleProxyProtocol unmarshals Proxy Protocol version (0, 1, 2) from int, string ("v1", "v2", "1", "2", "off"), or bool.
type FlexibleProxyProtocol int

func (p *FlexibleProxyProtocol) UnmarshalJSON(data []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(data)), `"`))
	switch s {
	case "", "0", "false", "null", "off", "none", "disable", "disabled":
		*p = 0
		return nil
	case "1", "v1", "true":
		*p = 1
		return nil
	case "2", "v2":
		*p = 2
		return nil
	default:
		if n, err := strconv.Atoi(s); err == nil && (n == 0 || n == 1 || n == 2) {
			*p = FlexibleProxyProtocol(n)
			return nil
		}
		return fmt.Errorf("invalid proxy_protocol value %q (must be 0, 1, 2, v1, or v2)", string(data))
	}
}

func (p FlexibleProxyProtocol) Int() int {
	return int(p)
}

// NetworkSettings holds transport-specific settings for MX protocol.
type NetworkSettings struct {
	Path                   string                `json:"path,omitempty"`
	Host                   string                `json:"host,omitempty"`
	Headers                map[string]string     `json:"headers,omitempty"`
	ServiceName            string                `json:"serviceName,omitempty"`
	Mode                   string                `json:"mode,omitempty"`
	Username               string                `json:"username,omitempty"`
	CertificateFingerprint string                `json:"certificateFingerprint,omitempty"`
	FakeTitle              string                `json:"fakeTitle,omitempty"`
	FakeMessage            string                `json:"fakeMessage,omitempty"`
	AcceptProxyProtocol    FlexibleBool          `json:"acceptProxyProtocol,omitempty"`
	ProxyProtocol          FlexibleProxyProtocol `json:"proxyProtocol,omitempty"`
	ForwardAddress         string                `json:"forwardAddress,omitempty"`
	UseTLSCertificate      FlexibleBool          `json:"useTLSCertificate,omitempty"`
	CIDRSegments           StringSlice           `json:"cidrSegments,omitempty"`
	MaxEarlyData           int32                 `json:"maxEarlyData,omitempty"`
	EarlyDataHeaderName    string                `json:"earlyDataHeaderName,omitempty"`
	Split                  any                   `json:"split,omitempty"`
}

func (n *NetworkSettings) UnmarshalJSON(data []byte) error {
	if isEmptyJSONArrayOrNull(data) {
		*n = NetworkSettings{}
		return nil
	}
	type plain NetworkSettings
	var raw struct {
		plain
		ServiceNameSnake            string                 `json:"service_name"`
		CertificateFingerprintSnake string                 `json:"certificate_fingerprint"`
		FakeTitleSnake              string                 `json:"fake_title"`
		FakeMessageSnake            string                 `json:"fake_message"`
		AcceptProxyProtocolSnake    *FlexibleBool          `json:"accept_proxy_protocol"`
		ProxyProtocolSnake          *FlexibleProxyProtocol `json:"proxy_protocol"`
		SendProxyProtocolCamel      *FlexibleProxyProtocol `json:"sendProxyProtocol"`
		SendProxyProtocolSnake      *FlexibleProxyProtocol `json:"send_proxy_protocol"`
		ForwardAddressSnake         string                 `json:"forward_address"`
		UseTLSCertificateSnake      *FlexibleBool          `json:"use_tls_certificate"`
		CIDRSegmentsSnake           StringSlice            `json:"cidr_segments"`
		MaxEarlyDataSnake           int32                  `json:"max_early_data"`
		EarlyDataHeaderNameSnake    string                 `json:"early_data_header_name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := NetworkSettings(raw.plain)
	if out.ServiceName == "" && raw.ServiceNameSnake != "" {
		out.ServiceName = raw.ServiceNameSnake
	}
	if out.CertificateFingerprint == "" && raw.CertificateFingerprintSnake != "" {
		out.CertificateFingerprint = raw.CertificateFingerprintSnake
	}
	if out.FakeTitle == "" && raw.FakeTitleSnake != "" {
		out.FakeTitle = raw.FakeTitleSnake
	}
	if out.FakeMessage == "" && raw.FakeMessageSnake != "" {
		out.FakeMessage = raw.FakeMessageSnake
	}
	if !out.AcceptProxyProtocol.Bool() && raw.AcceptProxyProtocolSnake != nil {
		out.AcceptProxyProtocol = *raw.AcceptProxyProtocolSnake
	}
	if out.ProxyProtocol == 0 {
		switch {
		case raw.ProxyProtocolSnake != nil && *raw.ProxyProtocolSnake > 0:
			out.ProxyProtocol = *raw.ProxyProtocolSnake
		case raw.SendProxyProtocolCamel != nil && *raw.SendProxyProtocolCamel > 0:
			out.ProxyProtocol = *raw.SendProxyProtocolCamel
		case raw.SendProxyProtocolSnake != nil && *raw.SendProxyProtocolSnake > 0:
			out.ProxyProtocol = *raw.SendProxyProtocolSnake
		}
	}
	if out.ForwardAddress == "" && raw.ForwardAddressSnake != "" {
		out.ForwardAddress = raw.ForwardAddressSnake
	}
	if !out.UseTLSCertificate.Bool() && raw.UseTLSCertificateSnake != nil {
		out.UseTLSCertificate = *raw.UseTLSCertificateSnake
	}
	if len(out.CIDRSegments) == 0 && len(raw.CIDRSegmentsSnake) > 0 {
		out.CIDRSegments = raw.CIDRSegmentsSnake
	}
	if out.MaxEarlyData == 0 && raw.MaxEarlyDataSnake > 0 {
		out.MaxEarlyData = raw.MaxEarlyDataSnake
	}
	if out.EarlyDataHeaderName == "" && raw.EarlyDataHeaderNameSnake != "" {
		out.EarlyDataHeaderName = raw.EarlyDataHeaderNameSnake
	}
	*n = out
	return nil
}

// TLSSettings holds TLS or REALITY security settings for MX protocol.
type TLSSettings struct {
	ServerName    string       `json:"server_name,omitempty"`
	AllowInsecure FlexibleBool `json:"allow_insecure,omitempty"`
	Fingerprint   string       `json:"fingerprint,omitempty"`
	PublicKey     string       `json:"public_key,omitempty"`
	PrivateKey    string       `json:"private_key,omitempty"`
	ShortID       string       `json:"short_id,omitempty"`
	ServerPort    int          `json:"server_port,omitempty"`
	CertFile      string       `json:"cert_file,omitempty"`
	KeyFile       string       `json:"key_file,omitempty"`
}

func (t *TLSSettings) UnmarshalJSON(data []byte) error {
	if isEmptyJSONArrayOrNull(data) {
		*t = TLSSettings{}
		return nil
	}
	type plain TLSSettings
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*t = TLSSettings(p)
	return nil
}

// BaseConfig contains push/pull interval settings returned by Xboard.
type BaseConfig struct {
	PushInterval int `json:"push_interval"`
	PullInterval int `json:"pull_interval"`
}

func (b *BaseConfig) UnmarshalJSON(data []byte) error {
	if isEmptyJSONArrayOrNull(data) {
		*b = BaseConfig{}
		return nil
	}
	type plain BaseConfig
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = BaseConfig(p)
	return nil
}

// RouteRule represents a server routing rule from Xboard.
type RouteRule struct {
	ID          int         `json:"id"`
	Match       StringSlice `json:"match"`
	Action      string      `json:"action"`
	ActionValue string      `json:"action_value"`
}

// CertConfig represents optional certificate configuration from Xboard.
type CertConfig struct {
	CertMode   string `json:"cert_mode,omitempty"`
	CertDomain string `json:"cert_domain,omitempty"`
	CertFile   string `json:"cert_file,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	CertPem    string `json:"cert_pem,omitempty"`
	KeyPem     string `json:"key_pem,omitempty"`
}

func (c *CertConfig) UnmarshalJSON(data []byte) error {
	if isEmptyJSONArrayOrNull(data) {
		*c = CertConfig{}
		return nil
	}
	type plain CertConfig
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*c = CertConfig(p)
	return nil
}

// NodeConfig represents the full node configuration returned by Xboard UniProxy/Server config endpoint.
type NodeConfig struct {
	Host            string          `json:"host"`
	ListenIP        string          `json:"listen_ip"`
	ServerPort      int             `json:"server_port"`
	Network         string          `json:"network"`
	NetworkSettings NetworkSettings `json:"network_settings"`
	Protocol        string          `json:"protocol"`
	TLS             int             `json:"tls"`
	TLSSettings     TLSSettings     `json:"tls_settings"`
	ServerName      string          `json:"server_name"`
	AllowInsecure   FlexibleBool    `json:"allow_insecure"`
	BaseConfig      BaseConfig      `json:"base_config"`
	Routes          []RouteRule     `json:"routes,omitempty"`
	CustomOutbounds any             `json:"custom_outbounds,omitempty"`
	CustomRoutes    any             `json:"custom_routes,omitempty"`
	CertConfig      CertConfig      `json:"cert_config,omitempty"`
}

func (nc *NodeConfig) UnmarshalJSON(data []byte) error {
	type alias NodeConfig
	var raw struct {
		alias
		NetworkSettingsCamel *NetworkSettings `json:"networkSettings"`
		TLSSettingsCamel     *TLSSettings     `json:"tlsSettings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*nc = NodeConfig(raw.alias)
	if isEmptyNetworkSettings(nc.NetworkSettings) && raw.NetworkSettingsCamel != nil {
		nc.NetworkSettings = *raw.NetworkSettingsCamel
	}
	if isEmptyTLSSettings(nc.TLSSettings) && raw.TLSSettingsCamel != nil {
		nc.TLSSettings = *raw.TLSSettingsCamel
	}
	nc.Normalize()
	return nil
}

// Normalize consolidates top-level and nested TLS/network fields and applies defaults.
func (nc *NodeConfig) Normalize() {
	if nc.ListenIP == "" {
		nc.ListenIP = "0.0.0.0"
	}
	nc.Network = strings.ToLower(strings.TrimSpace(nc.Network))
	if nc.Network == "" {
		nc.Network = "tcp"
	}
	if nc.Protocol == "" {
		nc.Protocol = "mx"
	}
	if nc.TLSSettings.ServerName == "" && nc.ServerName != "" {
		nc.TLSSettings.ServerName = nc.ServerName
	}
	if nc.ServerName == "" && nc.TLSSettings.ServerName != "" {
		nc.ServerName = nc.TLSSettings.ServerName
	}
	if !nc.TLSSettings.AllowInsecure.Bool() && nc.AllowInsecure.Bool() {
		nc.TLSSettings.AllowInsecure = nc.AllowInsecure
	}
}

func isEmptyNetworkSettings(n NetworkSettings) bool {
	return n.Path == "" && n.Host == "" && len(n.Headers) == 0 &&
		n.ServiceName == "" && n.Mode == "" && n.Username == "" &&
		n.CertificateFingerprint == "" && n.FakeTitle == "" &&
		n.FakeMessage == "" && !n.AcceptProxyProtocol.Bool() &&
		n.ProxyProtocol == 0 && n.ForwardAddress == "" &&
		!n.UseTLSCertificate.Bool() && len(n.CIDRSegments) == 0 &&
		n.MaxEarlyData == 0 && n.EarlyDataHeaderName == "" && n.Split == nil
}

func isEmptyTLSSettings(t TLSSettings) bool {
	return t.ServerName == "" && !t.AllowInsecure.Bool() && t.Fingerprint == "" &&
		t.PublicKey == "" && t.PrivateKey == "" && t.ShortID == "" && t.ServerPort == 0 &&
		t.CertFile == "" && t.KeyFile == ""
}

// User represents an active user returned by GET /api/v1/server/UniProxy/user.
type User struct {
	ID          int    `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

func (u *User) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          int    `json:"id"`
		UUID        string `json:"uuid"`
		SpeedLimit  *int   `json:"speed_limit"`
		DeviceLimit *int   `json:"device_limit"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.ID = raw.ID
	u.UUID = strings.TrimSpace(raw.UUID)
	if raw.SpeedLimit != nil && *raw.SpeedLimit > 0 {
		u.SpeedLimit = *raw.SpeedLimit
	}
	if raw.DeviceLimit != nil && *raw.DeviceLimit > 0 {
		u.DeviceLimit = *raw.DeviceLimit
	}
	return nil
}

// UsersResponse wraps the response from GET user.
type UsersResponse struct {
	Users []User `json:"users"`
}

// AliveListResponse wraps the response from GET alivelist.
type AliveListResponse struct {
	Alive map[int]int `json:"alive"`
}

func (a *AliveListResponse) UnmarshalJSON(data []byte) error {
	var wrapper struct {
		Alive json.RawMessage `json:"alive"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	a.Alive = make(map[int]int)
	if isEmptyJSONArrayOrNull(wrapper.Alive) || bytes.Equal(bytes.TrimSpace(wrapper.Alive), []byte("{}")) {
		return nil
	}
	var strMap map[string]int
	if err := json.Unmarshal(wrapper.Alive, &strMap); err != nil {
		return err
	}
	for k, v := range strMap {
		if uid, err := strconv.Atoi(k); err == nil {
			a.Alive[uid] = v
		}
	}
	return nil
}

// UsageCounter represents total/used byte counts for mem, swap, and disk.
type UsageCounter struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// NodeStatus represents the payload sent to POST status.
type NodeStatus struct {
	CPU  float64      `json:"cpu"`
	Mem  UsageCounter `json:"mem"`
	Swap UsageCounter `json:"swap"`
	Disk UsageCounter `json:"disk"`
}

// NodeReport represents the combined V2 report payload sent to POST /api/v2/server/report.
type NodeReport struct {
	Traffic map[string][2]int64 `json:"traffic,omitempty"`
	Alive   map[string][]string `json:"alive,omitempty"`
	Online  map[string]int      `json:"online,omitempty"`
	Status  *NodeStatus         `json:"status,omitempty"`
	Metrics map[string]any      `json:"metrics,omitempty"`
}

// MachineNodeInfo represents a node entry returned by POST /api/v2/server/machine/nodes.
type MachineNodeInfo struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// MachineNodesResponse represents the response from POST /api/v2/server/machine/nodes.
type MachineNodesResponse struct {
	Nodes      []MachineNodeInfo `json:"nodes"`
	BaseConfig BaseConfig        `json:"base_config"`
}

// ClientOptions configures a Panel Client.
type ClientOptions struct {
	PanelURL   string
	Token      string
	NodeID     int
	NodeType   string
	MachineID  int
	APIVersion string // "v1", "v2", or "auto"
	Timeout    time.Duration
}

// Client communicates with Xboard's server APIs (UniProxy V1 and Server V2).
type Client struct {
	opts       ClientOptions
	httpClient *http.Client

	mu         sync.Mutex
	configETag string
	userETag   string
	cachedCfg  *NodeConfig
	cachedUsrs []User
}

// NewClient creates a new Xboard panel API client with HTTP keep-alive connection pooling.
func NewClient(opts ClientOptions) *Client {
	opts.PanelURL = strings.TrimRight(strings.TrimSpace(opts.PanelURL), "/")
	if opts.NodeType == "" {
		opts.NodeType = "mx"
	}
	if opts.APIVersion == "" {
		opts.APIVersion = "v1"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	return &Client{
		opts: opts,
		httpClient: &http.Client{
			Timeout: opts.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// ResetETags clears cached ETags so the next pull fetches full payloads.
func (c *Client) ResetETags() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configETag = ""
	c.userETag = ""
}

func (c *Client) buildURL(action string, forceV1 bool) string {
	useV2 := !forceV1 && c.opts.APIVersion == "v2"
	basePath := "/api/v1/server/UniProxy/" + action
	if useV2 {
		basePath = "/api/v2/server/" + action
	}

	q := url.Values{}
	q.Set("token", c.opts.Token)
	if c.opts.NodeID > 0 {
		q.Set("node_id", strconv.Itoa(c.opts.NodeID))
	}
	if c.opts.NodeType != "" {
		q.Set("node_type", c.opts.NodeType)
	}
	if c.opts.MachineID > 0 && useV2 {
		q.Set("machine_id", strconv.Itoa(c.opts.MachineID))
	}
	return c.opts.PanelURL + basePath + "?" + q.Encode()
}

// GetNodeConfig fetches the node configuration from Xboard.
func (c *Client) GetNodeConfig(ctx context.Context) (*NodeConfig, bool, error) {
	c.mu.Lock()
	etag := c.configETag
	c.mu.Unlock()

	body, newETag, notModified, err := c.doGetWithETag(ctx, "config", etag)
	if err != nil {
		return nil, false, err
	}
	if notModified {
		c.mu.Lock()
		cached := c.cachedCfg
		c.mu.Unlock()
		return cached, false, nil
	}

	var cfg NodeConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, false, fmt.Errorf("decode node config: %w (body: %s)", err, truncateStr(string(body), 256))
	}
	if !strings.EqualFold(cfg.Protocol, "mx") {
		return nil, false, fmt.Errorf("unexpected protocol %q from panel (expected \"mx\")", cfg.Protocol)
	}

	c.mu.Lock()
	if newETag != "" {
		c.configETag = newETag
	}
	c.cachedCfg = &cfg
	c.mu.Unlock()

	return &cfg, true, nil
}

// GetUsers fetches the available user list for this node from Xboard.
func (c *Client) GetUsers(ctx context.Context) ([]User, bool, error) {
	c.mu.Lock()
	etag := c.userETag
	c.mu.Unlock()

	body, newETag, notModified, err := c.doGetWithETag(ctx, "user", etag)
	if err != nil {
		return nil, false, err
	}
	if notModified {
		c.mu.Lock()
		cached := c.cachedUsrs
		c.mu.Unlock()
		return cached, false, nil
	}

	var resp UsersResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, false, fmt.Errorf("decode users response: %w (body: %s)", err, truncateStr(string(body), 256))
	}

	c.mu.Lock()
	if newETag != "" {
		c.userETag = newETag
	}
	c.cachedUsrs = resp.Users
	c.mu.Unlock()

	return resp.Users, true, nil
}

// GetAliveList fetches the global per-user active device count from Xboard.
func (c *Client) GetAliveList(ctx context.Context) (map[int]int, error) {
	body, _, _, err := c.doGetWithETag(ctx, "alivelist", "")
	if err != nil {
		return nil, err
	}
	var resp AliveListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode alivelist response: %w", err)
	}
	return resp.Alive, nil
}

// PushTraffic reports per-user upload and download byte deltas to Xboard.
func (c *Client) PushTraffic(ctx context.Context, traffic map[int][2]int64) error {
	if len(traffic) == 0 {
		return nil
	}
	payload := make(map[string][2]int64, len(traffic))
	for uid, bytesPair := range traffic {
		if bytesPair[0] > 0 || bytesPair[1] > 0 {
			payload[strconv.Itoa(uid)] = bytesPair
		}
	}
	if len(payload) == 0 {
		return nil
	}
	return c.doPostJSON(ctx, "push", payload)
}

// PushAlive reports per-user online client IPs to Xboard.
func (c *Client) PushAlive(ctx context.Context, alive map[int][]string) error {
	if len(alive) == 0 {
		return nil
	}
	payload := make(map[string][]string, len(alive))
	for uid, ips := range alive {
		if len(ips) > 0 {
			payload[strconv.Itoa(uid)] = ips
		}
	}
	if len(payload) == 0 {
		return nil
	}
	return c.doPostJSON(ctx, "alive", payload)
}

// PushStatus reports node CPU, memory, swap, and disk status to Xboard.
func (c *Client) PushStatus(ctx context.Context, status NodeStatus) error {
	return c.doPostJSON(ctx, "status", status)
}

// PushReport sends a combined V2 report to /api/v2/server/report.
func (c *Client) PushReport(ctx context.Context, report NodeReport) error {
	q := url.Values{}
	q.Set("token", c.opts.Token)
	q.Set("node_id", strconv.Itoa(c.opts.NodeID))
	q.Set("node_type", c.opts.NodeType)
	if c.opts.MachineID > 0 {
		q.Set("machine_id", strconv.Itoa(c.opts.MachineID))
	}
	return c.doPostRawURL(ctx, c.opts.PanelURL+"/api/v2/server/report?"+q.Encode(), report)
}

// GetMachineNodes fetches all enabled nodes assigned to a machine via POST /api/v2/server/machine/nodes.
func (c *Client) GetMachineNodes(ctx context.Context) (*MachineNodesResponse, error) {
	if c.opts.MachineID <= 0 {
		return nil, errors.New("machine_id is required for GetMachineNodes")
	}
	reqBody := map[string]any{
		"machine_id": c.opts.MachineID,
		"token":      c.opts.Token,
	}
	rawResp, err := c.doPostRawURLWithResponse(ctx, c.opts.PanelURL+"/api/v2/server/machine/nodes", reqBody)
	if err != nil {
		return nil, err
	}
	var out MachineNodesResponse
	if err := json.Unmarshal(rawResp, &out); err != nil {
		return nil, fmt.Errorf("decode machine nodes: %w", err)
	}
	return &out, nil
}

// PushMachineStatus reports machine-level status via POST /api/v2/server/machine/status.
func (c *Client) PushMachineStatus(ctx context.Context, status NodeStatus) error {
	if c.opts.MachineID <= 0 {
		return errors.New("machine_id is required for PushMachineStatus")
	}
	q := url.Values{}
	q.Set("machine_id", strconv.Itoa(c.opts.MachineID))
	q.Set("token", c.opts.Token)
	return c.doPostRawURL(ctx, c.opts.PanelURL+"/api/v2/server/machine/status?"+q.Encode(), status)
}

func (c *Client) doGetWithETag(ctx context.Context, action string, etag string) ([]byte, string, bool, error) {
	targetURL := c.buildURL(action, false)
	body, newETag, notMod, status, err := c.executeGet(ctx, targetURL, etag)
	if err != nil && c.opts.APIVersion == "auto" && status >= 400 {
		if targetURLV1 := c.buildURL(action, true); targetURLV1 != targetURL {
			body, newETag, notMod, _, err = c.executeGet(ctx, targetURLV1, etag)
		}
	}
	return body, newETag, notMod, err
}

func (c *Client) executeGet(ctx context.Context, targetURL string, etag string) ([]byte, string, bool, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, "", false, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Skyline-MX-Backend/1.0")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		respETag := resp.Header.Get("ETag")
		if respETag == "" {
			respETag = etag
		}
		return nil, respETag, true, resp.StatusCode, nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", false, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", false, resp.StatusCode, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, sanitizeURL(targetURL), truncateStr(string(data), 256))
	}
	return data, resp.Header.Get("ETag"), false, resp.StatusCode, nil
}

func (c *Client) doPostJSON(ctx context.Context, action string, payload any) error {
	return c.doPostRawURL(ctx, c.buildURL(action, false), payload)
}

func (c *Client) doPostRawURL(ctx context.Context, targetURL string, payload any) error {
	_, err := c.doPostRawURLWithResponse(ctx, targetURL, payload)
	return err
}

func (c *Client) doPostRawURLWithResponse(ctx context.Context, targetURL string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request json: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Skyline-MX-Backend/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, sanitizeURL(targetURL), truncateStr(string(body), 256))
	}
	return body, nil
}

func sanitizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if q.Has("token") {
		q.Set("token", "***")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func truncateStr(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
