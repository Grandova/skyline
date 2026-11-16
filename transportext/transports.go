package transportext

import (
	"bufio"
	"bytes"
	"context"
	gotls "crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/types/known/wrapperspb"

	v2net "github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"github.com/v2fly/v2ray-core/v5/transport/internet"
	v2tls "github.com/v2fly/v2ray-core/v5/transport/internet/tls"

	"skyline/mx"
)

const (
	ProtocolWS       = "mxws"
	ProtocolXHTTP    = "xhttp"
	ProtocolJATP     = "jatp"
	ProtocolMC1      = "mc1"
	ProtocolMundoRDP = "mundordp"
)

// ExtendedTransportSettings carries JSON-encoded transport parameters inside wrapperspb.StringValue.
type ExtendedTransportSettings struct {
	Network                string            `json:"network"`
	Path                   string            `json:"path,omitempty"`
	Host                   string            `json:"host,omitempty"`
	Headers                map[string]string `json:"headers,omitempty"`
	Mode                   string            `json:"mode,omitempty"`
	Username               string            `json:"username,omitempty"`
	CertificateFingerprint string            `json:"certificateFingerprint,omitempty"`
	FakeTitle              string            `json:"fakeTitle,omitempty"`
	FakeMessage            string            `json:"fakeMessage,omitempty"`
	AcceptProxyProtocol    bool              `json:"acceptProxyProtocol,omitempty"`
	ProxyProtocol          int               `json:"proxyProtocol,omitempty"`
	UseTLSCertificate      bool              `json:"useTLSCertificate,omitempty"`
	CIDRSegments           []string          `json:"cidrSegments,omitempty"`
	MaxEarlyData           int32             `json:"maxEarlyData,omitempty"`
	EarlyDataHeaderName    string            `json:"earlyDataHeaderName,omitempty"`
}

// EncodeTransportSettings wraps ExtendedTransportSettings in a protobuf StringValue Any message.
func EncodeTransportSettings(s ExtendedTransportSettings) (*internet.TransportConfig, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	msg := &wrapperspb.StringValue{Value: string(raw)}
	return &internet.TransportConfig{
		ProtocolName: s.Network,
		Settings:     serial.ToTypedMessage(msg),
	}, nil
}

func decodeTransportSettings(streamSettings *internet.MemoryStreamConfig) ExtendedTransportSettings {
	var out ExtendedTransportSettings
	if streamSettings == nil || streamSettings.ProtocolSettings == nil {
		return out
	}
	if sv, ok := streamSettings.ProtocolSettings.(*wrapperspb.StringValue); ok && sv != nil && sv.Value != "" {
		_ = json.Unmarshal([]byte(sv.Value), &out)
	}
	return out
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

func decodeBase64EarlyData(raw string) ([]byte, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(raw); err == nil && len(b) > 0 {
			return b, true
		}
	}
	return nil, false
}

var registerOnce sync.Once

// RegisterAll registers mxws, xhttp, jatp, mc1, and mundordp listeners and dialers into v2ray-core's internet package.
func RegisterAll() {
	registerOnce.Do(func() {
		for _, protoName := range []string{ProtocolWS, ProtocolXHTTP, ProtocolJATP, ProtocolMC1, ProtocolMundoRDP} {
			_ = internet.RegisterProtocolConfigCreator(protoName, func() interface{} {
				return &wrapperspb.StringValue{}
			})
		}
		_ = internet.RegisterTransportListener(ProtocolWS, listenHTTPStreamTransport)
		_ = internet.RegisterTransportListener(ProtocolXHTTP, listenHTTPStreamTransport)
		_ = internet.RegisterTransportListener(ProtocolJATP, listenHTTPStreamTransport)
		_ = internet.RegisterTransportListener(ProtocolMC1, listenMC1Transport)
		_ = internet.RegisterTransportListener(ProtocolMundoRDP, listenMundoRDPTransport)

		_ = internet.RegisterTransportDialer(ProtocolWS, dialWSTransport)
		_ = internet.RegisterTransportDialer(ProtocolXHTTP, dialHTTPStreamTransport)
		_ = internet.RegisterTransportDialer(ProtocolJATP, dialHTTPStreamTransport)
		_ = internet.RegisterTransportDialer(ProtocolMC1, dialMC1Transport)
		_ = internet.RegisterTransportDialer(ProtocolMundoRDP, dialMundoRDPTransport)
	})
}

func init() {
	RegisterAll()
}

// evaluateFastAuth validates the MundoConnect-Auth request header if present.
func evaluateFastAuth(ctx context.Context, r *http.Request, respHeader http.Header) (*mx.FastAuthResult, string, *protocol.MemoryUser, v2net.Destination) {
	raw := r.Header.Get(mx.FastAuthRequestHeader)
	if raw == "" {
		return nil, "", nil, v2net.Destination{}
	}
	payload, err := mx.DecodeFastAuthPayload(raw)
	if err != nil || payload == nil {
		respHeader.Set(mx.FastAuthResponseHeader, mx.FastAuthStatusFail)
		return nil, "", nil, v2net.Destination{}
	}
	inbound, validateErr := mx.ValidateFastAuthGlobal(r.Context(), payload)
	if validateErr != nil || inbound == nil {
		respHeader.Set(mx.FastAuthResponseHeader, mx.FastAuthStatusFail)
		return nil, "", nil, v2net.Destination{}
	}
	respHeader.Set(mx.FastAuthResponseHeader, mx.FastAuthStatusOK)
	user, _ := inbound.User.(*protocol.MemoryUser)
	target, _ := inbound.Target.(v2net.Destination)
	return &mx.FastAuthResult{Status: mx.FastAuthStatusOK}, strings.TrimSpace(payload.AuthData), user, target
}

func wrapFastAuthConn(base v2net.Conn, result *mx.FastAuthResult, token string, user *protocol.MemoryUser, target v2net.Destination) internet.Connection {
	if result != nil && result.Status == mx.FastAuthStatusOK {
		return &mx.FastAuthConnWrapper{
			Connection:       base,
			FastAuthProtocol: mx.FastAuthProtocol,
			FastAuthToken:    token,
			FastAuthUser:     user,
			FastAuthTarget:   target,
			FastAuthResult:   result,
		}
	}
	return base
}

func parseRemoteAddr(r *http.Request, fallback v2net.Addr) v2net.Addr {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		first := strings.TrimSpace(strings.Split(xff, ",")[0])
		if addr := v2net.ParseAddress(first); addr != nil && addr.Family().IsIP() {
			return &v2net.TCPAddr{IP: addr.IP(), Port: 0}
		}
	}
	if host, portStr, err := v2net.SplitHostPort(r.RemoteAddr); err == nil {
		if addr := v2net.ParseAddress(host); addr != nil && addr.Family().IsIP() {
			p, _ := v2net.PortFromString(portStr)
			return &v2net.TCPAddr{IP: addr.IP(), Port: int(p)}
		}
	}
	return fallback
}

// ============================================================================
// XHTTP / JATP & MC1 HTTP/WebSocket Full-Duplex Transport
// ============================================================================

type httpStreamListener struct {
	ctx      context.Context
	listener v2net.Listener
	server   *http.Server
	addConn  internet.ConnHandler
	cfg      ExtendedTransportSettings

	splitMu       sync.Mutex
	splitSessions map[string]*splitSession
}

type splitSession struct {
	downReader *io.PipeReader
	downWriter *io.PipeWriter
	upReader   *io.PipeReader
	upWriter   *io.PipeWriter
	remoteAddr v2net.Addr
	localAddr  v2net.Addr
	created    time.Time
}

func (l *httpStreamListener) Addr() v2net.Addr {
	return l.listener.Addr()
}

func (l *httpStreamListener) Close() error {
	l.splitMu.Lock()
	for id, sess := range l.splitSessions {
		delete(l.splitSessions, id)
		_ = sess.upReader.Close()
		_ = sess.upWriter.Close()
		_ = sess.downReader.Close()
		_ = sess.downWriter.Close()
	}
	l.splitMu.Unlock()
	_ = l.server.Close()
	return l.listener.Close()
}

var wsUpgrader = &websocket.Upgrader{
	ReadBufferSize:   8 * 1024,
	WriteBufferSize:  8 * 1024,
	HandshakeTimeout: 5 * time.Second,
	CheckOrigin:      func(r *http.Request) bool { return true },
}

func listenHTTPStreamTransport(ctx context.Context, address v2net.Address, port v2net.Port, streamSettings *internet.MemoryStreamConfig, addConn internet.ConnHandler) (internet.Listener, error) {
	return createHTTPBasedListener(ctx, address, port, streamSettings, addConn)
}

func listenMC1Transport(ctx context.Context, address v2net.Address, port v2net.Port, streamSettings *internet.MemoryStreamConfig, addConn internet.ConnHandler) (internet.Listener, error) {
	return createHTTPBasedListener(ctx, address, port, streamSettings, addConn)
}

func createHTTPBasedListener(ctx context.Context, address v2net.Address, port v2net.Port, streamSettings *internet.MemoryStreamConfig, addConn internet.ConnHandler) (internet.Listener, error) {
	cfg := decodeTransportSettings(streamSettings)
	if streamSettings.SocketSettings == nil {
		streamSettings.SocketSettings = &internet.SocketConfig{}
	}
	if cfg.AcceptProxyProtocol {
		streamSettings.SocketSettings.AcceptProxyProtocol = true
	}

	rawListener, err := internet.ListenSystem(ctx, &v2net.TCPAddr{
		IP:   address.IP(),
		Port: int(port),
	}, streamSettings.SocketSettings)
	if err != nil {
		return nil, err
	}
	rawListener = WrapProxyProtocolListener(rawListener, streamSettings.SocketSettings.AcceptProxyProtocol)

	if tlsCfg := v2tls.ConfigFromStreamSettings(streamSettings); tlsCfg != nil {
		if stdTLS := tlsCfg.GetTLSConfig(); stdTLS != nil {
			rawListener = gotls.NewListener(rawListener, stdTLS)
		}
	}

	hl := &httpStreamListener{
		ctx:           ctx,
		listener:      rawListener,
		addConn:       addConn,
		cfg:           cfg,
		splitSessions: make(map[string]*splitSession),
	}

	hl.server = &http.Server{
		Handler:           http.HandlerFunc(hl.serveHTTP),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		_ = hl.server.Serve(rawListener)
	}()

	return hl, nil
}

func (l *httpStreamListener) serveHTTP(w http.ResponseWriter, r *http.Request) {
	expectedPath := normalizePath(l.cfg.Path)
	reqPath := normalizePath(r.URL.Path)
	if expectedPath != "/" && reqPath != expectedPath && !strings.HasPrefix(reqPath, expectedPath+"/") {
		http.NotFound(w, r)
		return
	}

	// 1. Check if client is requesting a WebSocket upgrade (used by mc1 or ws-compatible clients)
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		respHeader := http.Header{}
		var earlyData []byte
		if l.cfg.EarlyDataHeaderName != "" {
			if ed := strings.TrimSpace(r.Header.Get(l.cfg.EarlyDataHeaderName)); ed != "" {
				if decoded, ok := decodeBase64EarlyData(ed); ok {
					earlyData = decoded
					if strings.EqualFold(l.cfg.EarlyDataHeaderName, "Sec-WebSocket-Protocol") {
						respHeader.Set("Sec-WebSocket-Protocol", ed)
					}
				}
			}
		} else if wsap := r.Header.Values("Sec-WebSocket-Protocol"); len(wsap) > 0 {
			rawED := strings.TrimSpace(strings.Split(wsap[0], ",")[0])
			if rawED != "" {
				if decoded, ok := decodeBase64EarlyData(rawED); ok {
					earlyData = decoded
					respHeader.Set("Sec-WebSocket-Protocol", rawED)
				}
			}
		}
		if len(earlyData) == 0 && expectedPath != "/" && strings.HasPrefix(reqPath, expectedPath+"/") {
			suffix := strings.TrimPrefix(reqPath, expectedPath+"/")
			if suffix != "" {
				if decoded, ok := decodeBase64EarlyData(suffix); ok {
					earlyData = decoded
				}
			}
		}

		faRes, faToken, faUser, faTarget := evaluateFastAuth(l.ctx, r, respHeader)
		wsConn, err := wsUpgrader.Upgrade(w, r, respHeader)
		if err != nil {
			return
		}
		remoteAddr := parseRemoteAddr(r, wsConn.RemoteAddr())
		baseConn := newWSStreamConn(wsConn, remoteAddr)
		if len(earlyData) > 0 {
			baseConn.reader = bytes.NewReader(earlyData)
		}
		l.addConn(wrapFastAuthConn(baseConn, faRes, faToken, faUser, faTarget))
		return
	}

	// 2. Check if client is using split-HTTP session mode (?session=<id> or X-Session-Id)
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Session-Id")
	}
	if sessionID != "" {
		l.handleSplitHTTP(w, r, sessionID)
		return
	}

	// 3. Full-duplex HTTP streaming (POST / PUT or HTTP Upgrade: xhttp/mc1)
	faRes, faToken, faUser, faTarget := evaluateFastAuth(l.ctx, r, w.Header())
	remoteAddr := parseRemoteAddr(r, l.listener.Addr())

	if strings.EqualFold(r.Header.Get("Connection"), "Upgrade") && r.Header.Get("Upgrade") != "" {
		hj, ok := w.(http.Hijacker)
		if ok {
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Upgrade", r.Header.Get("Upgrade"))
			w.WriteHeader(http.StatusSwitchingProtocols)
			rawConn, rw, err := hj.Hijack()
			if err == nil {
				baseConn := newHijackedConn(rawConn, rw, remoteAddr)
				l.addConn(wrapFastAuthConn(baseConn, faRes, faToken, faUser, faTarget))
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	doneCh := make(chan struct{})
	baseConn := &httpDuplexConn{
		reader:     r.Body,
		writer:     w,
		flusher:    w,
		localAddr:  l.listener.Addr(),
		remoteAddr: remoteAddr,
		doneCh:     doneCh,
	}
	l.addConn(wrapFastAuthConn(baseConn, faRes, faToken, faUser, faTarget))
	<-doneCh
}

func (l *httpStreamListener) handleSplitHTTP(w http.ResponseWriter, r *http.Request, sessionID string) {
	now := time.Now()
	l.splitMu.Lock()
	for id, s := range l.splitSessions {
		if now.Sub(s.created) > 2*time.Minute {
			delete(l.splitSessions, id)
			_ = s.upReader.Close()
			_ = s.upWriter.Close()
			_ = s.downReader.Close()
			_ = s.downWriter.Close()
		}
	}
	sess, exists := l.splitSessions[sessionID]
	if !exists {
		upR, upW := io.Pipe()
		downR, downW := io.Pipe()
		sess = &splitSession{
			downReader: downR,
			downWriter: downW,
			upReader:   upR,
			upWriter:   upW,
			remoteAddr: parseRemoteAddr(r, l.listener.Addr()),
			localAddr:  l.listener.Addr(),
			created:    time.Now(),
		}
		l.splitSessions[sessionID] = sess
		l.splitMu.Unlock()

		faRes, faToken, faUser, faTarget := evaluateFastAuth(l.ctx, r, w.Header())
		conn := &pipeDuplexConn{
			reader:     sess.upReader,
			writer:     sess.downWriter,
			localAddr:  sess.localAddr,
			remoteAddr: sess.remoteAddr,
			onClose: func() {
				l.splitMu.Lock()
				delete(l.splitSessions, sessionID)
				l.splitMu.Unlock()
				_ = sess.upReader.Close()
				_ = sess.upWriter.Close()
				_ = sess.downReader.Close()
				_ = sess.downWriter.Close()
			},
		}
		l.addConn(wrapFastAuthConn(conn, faRes, faToken, faUser, faTarget))
	} else {
		l.splitMu.Unlock()
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		buf := make([]byte, 16*1024)
		for {
			n, err := sess.downReader.Read(buf)
			if n > 0 {
				if _, wErr := w.Write(buf[:n]); wErr != nil {
					break
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return
	}

	// POST / PUT uplink chunk
	_, _ = io.Copy(sess.upWriter, r.Body)
	w.WriteHeader(http.StatusOK)
}

// ============================================================================
// MundoRDP Transport (RDP X.224 TPKT + HTTP/FastAuth + Raw TCP Stream)
// ============================================================================

var (
	// Standard RDP X.224 Connection Confirm TPKT (19 bytes) with SSL/Hybrid negotiation response
	rdpX224ConfirmPacket = []byte{
		0x03, 0x00, 0x00, 0x13, // TPKT Header: Version 3, Reserved 0, Length 19
		0x0e,                   // X.224 Length Indicator (14)
		0xd0,                   // X.224 Connection Confirm (0xd0)
		0x00, 0x00,             // DST-REF
		0x12, 0x34,             // SRC-REF
		0x00,                   // Class 0
		0x02, 0x01, 0x08, 0x00, // RDP Negotiation Response (Type 0x02, Flags 0x01, Length 8)
		0x00, 0x00, 0x00, 0x00, // Selected Protocol: Standard/SSL
	}
)

type mundoRDPListener struct {
	ctx      context.Context
	listener v2net.Listener
	addConn  internet.ConnHandler
	cfg      ExtendedTransportSettings
	tlsCfg   *gotls.Config
}

func (l *mundoRDPListener) Addr() v2net.Addr {
	return l.listener.Addr()
}

func (l *mundoRDPListener) Close() error {
	return l.listener.Close()
}

func listenMundoRDPTransport(ctx context.Context, address v2net.Address, port v2net.Port, streamSettings *internet.MemoryStreamConfig, addConn internet.ConnHandler) (internet.Listener, error) {
	cfg := decodeTransportSettings(streamSettings)
	if streamSettings.SocketSettings == nil {
		streamSettings.SocketSettings = &internet.SocketConfig{}
	}
	if cfg.AcceptProxyProtocol {
		streamSettings.SocketSettings.AcceptProxyProtocol = true
	}

	rawListener, err := internet.ListenSystem(ctx, &v2net.TCPAddr{
		IP:   address.IP(),
		Port: int(port),
	}, streamSettings.SocketSettings)
	if err != nil {
		return nil, err
	}
	rawListener = WrapProxyProtocolListener(rawListener, streamSettings.SocketSettings.AcceptProxyProtocol)

	var stdTLS *gotls.Config
	if tlsCfg := v2tls.ConfigFromStreamSettings(streamSettings); tlsCfg != nil {
		stdTLS = tlsCfg.GetTLSConfig()
	}

	ml := &mundoRDPListener{
		ctx:      ctx,
		listener: rawListener,
		addConn:  addConn,
		cfg:      cfg,
		tlsCfg:   stdTLS,
	}
	go ml.acceptLoop()
	return ml, nil
}

func (l *mundoRDPListener) acceptLoop() {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "closed") {
				return
			}
			continue
		}
		go l.handleConn(conn)
	}
}

func (l *mundoRDPListener) handleConn(conn v2net.Conn) {
	// If TLS is configured directly at connection start, peek first to see if client sent plaintext X.224 pre-TLS handshake
	br := bufio.NewReaderSize(conn, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	peek, err := br.Peek(4)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.Close()
		return
	}

	var activeConn v2net.Conn = &bufferedConn{Conn: conn, r: br}

	// Case 1: RDP X.224 TPKT header (0x03 0x00 <len_hi> <len_lo>) BEFORE or WITHOUT TLS
	if peek[0] == 0x03 && peek[1] == 0x00 {
		pktLen := int(peek[2])<<8 | int(peek[3])
		if pktLen >= 4 && pktLen <= 1024 {
			discard := make([]byte, pktLen)
			if _, err := io.ReadFull(activeConn, discard); err != nil {
				_ = activeConn.Close()
				return
			}
			if _, err := activeConn.Write(rdpX224ConfirmPacket); err != nil {
				_ = activeConn.Close()
				return
			}
		}
		if l.tlsCfg != nil {
			tlsConn := gotls.Server(activeConn, l.tlsCfg)
			if err := tlsConn.Handshake(); err != nil {
				_ = activeConn.Close()
				return
			}
			activeConn = tlsConn
		}
		l.addConn(activeConn)
		return
	}

	// Case 2: TLS ClientHello (0x16 0x03 ...)
	if l.tlsCfg != nil && peek[0] == 0x16 && peek[1] == 0x03 {
		tlsConn := gotls.Server(activeConn, l.tlsCfg)
		if err := tlsConn.Handshake(); err != nil {
			_ = activeConn.Close()
			return
		}
		innerBR := bufio.NewReaderSize(tlsConn, 4096)
		_ = tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		innerPeek, pErr := innerBR.Peek(4)
		_ = tlsConn.SetReadDeadline(time.Time{})
		activeConn = &bufferedConn{Conn: tlsConn, r: innerBR}
		if pErr == nil && len(innerPeek) >= 4 && innerPeek[0] == 0x03 && innerPeek[1] == 0x00 {
			pktLen := int(innerPeek[2])<<8 | int(innerPeek[3])
			if pktLen >= 4 && pktLen <= 1024 {
				discard := make([]byte, pktLen)
				if _, err := io.ReadFull(activeConn, discard); err != nil {
					_ = activeConn.Close()
					return
				}
				if _, err := activeConn.Write(rdpX224ConfirmPacket); err != nil {
					_ = activeConn.Close()
					return
				}
			}
		} else if pErr == nil && isHTTPPrefix(innerPeek) {
			l.handleMundoRDPHTTP(activeConn, innerBR)
			return
		}
		l.addConn(activeConn)
		return
	}

	// Case 3: Plaintext HTTP request (e.g. FastAuth Upgrade or probe)
	if isHTTPPrefix(peek) {
		l.handleMundoRDPHTTP(activeConn, br)
		return
	}

	l.addConn(activeConn)
}

func (l *mundoRDPListener) handleMundoRDPHTTP(conn v2net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = conn.Close()
		return
	}
	respHeader := make(http.Header)
	faRes, faToken, faUser, faTarget := evaluateFastAuth(l.ctx, req, respHeader)
	if faRes != nil && faRes.Status == mx.FastAuthStatusOK {
		var buf bytes.Buffer
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
		buf.WriteString("Connection: Upgrade\r\n")
		buf.WriteString("Upgrade: mundordp\r\n")
		buf.WriteString(fmt.Sprintf("%s: %s\r\n\r\n", mx.FastAuthResponseHeader, mx.FastAuthStatusOK))
		if _, err := conn.Write(buf.Bytes()); err != nil {
			_ = conn.Close()
			return
		}
		l.addConn(wrapFastAuthConn(conn, faRes, faToken, faUser, faTarget))
		return
	}

	// Return fake MundoRDP status page for unauthenticated HTTP probes
	title := l.cfg.FakeTitle
	if title == "" {
		title = "Remote Desktop Web Access"
	}
	msg := l.cfg.FakeMessage
	if msg == "" {
		msg = "MundoRDP Service Active"
	}
	body := fmt.Sprintf("<!DOCTYPE html><html><head><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>", title, title, msg)
	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	_, _ = conn.Write([]byte(resp))
	_ = conn.Close()
}

func isHTTPPrefix(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	s := string(b[:4])
	return s == "GET " || s == "POST" || s == "HEAD" || s == "PUT " || s == "OPTI"
}

// ============================================================================
// Client Dialers for XHTTP/JATP, MC1, and MundoRDP
// ============================================================================

func dialWSTransport(ctx context.Context, dest v2net.Destination, streamSettings *internet.MemoryStreamConfig) (internet.Connection, error) {
	cfg := decodeTransportSettings(streamSettings)
	scheme := "ws"
	wsDialer := &websocket.Dialer{
		NetDialContext: func(dCtx context.Context, network, addr string) (v2net.Conn, error) {
			raw, err := internet.DialSystem(dCtx, dest, streamSettings.SocketSettings)
			if err != nil {
				return nil, err
			}
			if cfg.ProxyProtocol == 1 || cfg.ProxyProtocol == 2 {
				if err := WriteProxyProtocolHeader(raw, cfg.ProxyProtocol, raw.LocalAddr(), raw.RemoteAddr()); err != nil {
					_ = raw.Close()
					return nil, err
				}
			}
			return raw, nil
		},
		ReadBufferSize:   8 * 1024,
		WriteBufferSize:  8 * 1024,
		HandshakeTimeout: 8 * time.Second,
	}
	if tlsCfg := v2tls.ConfigFromStreamSettings(streamSettings); tlsCfg != nil {
		scheme = "wss"
		wsDialer.NetDialTLSContext = func(dCtx context.Context, network, addr string) (v2net.Conn, error) {
			raw, err := internet.DialSystem(dCtx, dest, streamSettings.SocketSettings)
			if err != nil {
				return nil, err
			}
			if cfg.ProxyProtocol == 1 || cfg.ProxyProtocol == 2 {
				if err := WriteProxyProtocolHeader(raw, cfg.ProxyProtocol, raw.LocalAddr(), raw.RemoteAddr()); err != nil {
					_ = raw.Close()
					return nil, err
				}
			}
			stdTLS := tlsCfg.GetTLSConfig(v2tls.WithDestination(dest), v2tls.WithNextProto("http/1.1"))
			tlsConn := gotls.Client(raw, stdTLS)
			if err := tlsConn.HandshakeContext(dCtx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			return tlsConn, nil
		}
	}

	reqPath := normalizePath(cfg.Path)
	uri := fmt.Sprintf("%s://%s%s", scheme, dest.NetAddr(), reqPath)
	reqHeader := make(http.Header)
	if cfg.Host != "" {
		reqHeader.Set("Host", cfg.Host)
	}
	for k, v := range cfg.Headers {
		reqHeader.Set(k, v)
	}

	fastAuthReq := mx.PrepareFastAuthRequest(ctx)
	_ = fastAuthReq.SetHTTPHeaders(reqHeader)

	wsConn, resp, err := wsDialer.DialContext(ctx, uri, reqHeader)
	if err != nil {
		return nil, err
	}
	baseConn := newWSStreamConn(wsConn, wsConn.RemoteAddr())
	var respHeader http.Header
	if resp != nil {
		respHeader = resp.Header
	}
	observed := fastAuthReq.ObserveHeader(respHeader)
	if res := fastAuthReq.ConnectionResult(observed); res != nil && fastAuthReq.Payload != nil {
		return &mx.FastAuthConnWrapper{
			Connection:       baseConn,
			FastAuthProtocol: fastAuthReq.Payload.Protocol,
			FastAuthResult:   res,
		}, nil
	}
	return baseConn, nil
}

func dialHTTPStreamTransport(ctx context.Context, dest v2net.Destination, streamSettings *internet.MemoryStreamConfig) (internet.Connection, error) {
	return dialHTTPUpgrade(ctx, dest, streamSettings, ProtocolXHTTP)
}

func dialMC1Transport(ctx context.Context, dest v2net.Destination, streamSettings *internet.MemoryStreamConfig) (internet.Connection, error) {
	return dialHTTPUpgrade(ctx, dest, streamSettings, ProtocolMC1)
}

func dialHTTPUpgrade(ctx context.Context, dest v2net.Destination, streamSettings *internet.MemoryStreamConfig, upgradeProto string) (internet.Connection, error) {
	cfg := decodeTransportSettings(streamSettings)
	rawConn, err := internet.DialSystem(ctx, dest, streamSettings.SocketSettings)
	if err != nil {
		return nil, err
	}
	if cfg.ProxyProtocol == 1 || cfg.ProxyProtocol == 2 {
		if err := WriteProxyProtocolHeader(rawConn, cfg.ProxyProtocol, rawConn.LocalAddr(), rawConn.RemoteAddr()); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
	}
	if tlsCfg := v2tls.ConfigFromStreamSettings(streamSettings); tlsCfg != nil {
		stdTLS := tlsCfg.GetTLSConfig(v2tls.WithDestination(dest), v2tls.WithNextProto("http/1.1"))
		tlsConn := gotls.Client(rawConn, stdTLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		rawConn = tlsConn
	}

	reqPath := normalizePath(cfg.Path)
	hostHeader := cfg.Host
	if hostHeader == "" && len(cfg.Headers) > 0 {
		hostHeader = cfg.Headers["Host"]
	}
	if hostHeader == "" {
		hostHeader = dest.NetAddr()
	}

	u := &url.URL{Scheme: "http", Host: hostHeader, Path: reqPath}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", upgradeProto)
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	fastAuthReq := mx.PrepareFastAuthRequest(ctx)
	_ = fastAuthReq.SetHTTPHeaders(req.Header)

	if err := req.Write(rawConn); err != nil {
		_ = rawConn.Close()
		return nil, err
	}

	br := bufio.NewReaderSize(rawConn, 4096)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = rawConn.Close()
		return nil, fmt.Errorf("unexpected HTTP status %d in %s dial", resp.StatusCode, upgradeProto)
	}

	baseConn := &bufferedConn{Conn: rawConn, r: br}
	observed := fastAuthReq.ObserveHeader(resp.Header)
	if res := fastAuthReq.ConnectionResult(observed); res != nil && fastAuthReq.Payload != nil {
		return &mx.FastAuthConnWrapper{
			Connection:       baseConn,
			FastAuthProtocol: fastAuthReq.Payload.Protocol,
			FastAuthResult:   res,
		}, nil
	}
	return baseConn, nil
}

func dialMundoRDPTransport(ctx context.Context, dest v2net.Destination, streamSettings *internet.MemoryStreamConfig) (internet.Connection, error) {
	cfg := decodeTransportSettings(streamSettings)
	rawConn, err := internet.DialSystem(ctx, dest, streamSettings.SocketSettings)
	if err != nil {
		return nil, err
	}
	if cfg.ProxyProtocol == 1 || cfg.ProxyProtocol == 2 {
		if err := WriteProxyProtocolHeader(rawConn, cfg.ProxyProtocol, rawConn.LocalAddr(), rawConn.RemoteAddr()); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
	}

	username := strings.TrimSpace(cfg.Username)
	if username == "" {
		username = "MundoUser"
	}

	// Build RDP X.224 Connection Request TPKT with Cookie: mstshash=<username>\r\n
	cookie := fmt.Sprintf("Cookie: mstshash=%s\r\n", username)
	negReq := []byte{0x01, 0x00, 0x08, 0x00, 0x01, 0x00, 0x00, 0x00}
	x224DataLen := 6 + len(cookie) + len(negReq)
	totalLen := 4 + 1 + x224DataLen
	reqPkt := make([]byte, 0, totalLen)
	reqPkt = append(reqPkt, 0x03, 0x00, byte(totalLen>>8), byte(totalLen&0xff))
	reqPkt = append(reqPkt, byte(x224DataLen))
	reqPkt = append(reqPkt, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00)
	reqPkt = append(reqPkt, []byte(cookie)...)
	reqPkt = append(reqPkt, negReq...)

	if _, err := rawConn.Write(reqPkt); err != nil {
		_ = rawConn.Close()
		return nil, err
	}

	respHdr := make([]byte, 4)
	if _, err := io.ReadFull(rawConn, respHdr); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	if respHdr[0] != 0x03 || respHdr[1] != 0x00 {
		_ = rawConn.Close()
		return nil, fmt.Errorf("invalid MundoRDP X.224 response header")
	}
	remLen := (int(respHdr[2])<<8 | int(respHdr[3])) - 4
	if remLen > 0 {
		rem := make([]byte, remLen)
		if _, err := io.ReadFull(rawConn, rem); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
	}

	if tlsCfg := v2tls.ConfigFromStreamSettings(streamSettings); tlsCfg != nil {
		stdTLS := tlsCfg.GetTLSConfig(v2tls.WithDestination(dest))
		tlsConn := gotls.Client(rawConn, stdTLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		rawConn = tlsConn
	}

	return rawConn, nil
}

// ============================================================================
// Connection Adapters
// ============================================================================

type bufferedConn struct {
	v2net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

type hijackedConn struct {
	v2net.Conn
	rw         *bufio.ReadWriter
	remoteAddr v2net.Addr
}

func newHijackedConn(c v2net.Conn, rw *bufio.ReadWriter, remote v2net.Addr) *hijackedConn {
	return &hijackedConn{Conn: c, rw: rw, remoteAddr: remote}
}

func (c *hijackedConn) Read(p []byte) (int, error) {
	return c.rw.Read(p)
}

func (c *hijackedConn) Write(p []byte) (int, error) {
	n, err := c.rw.Write(p)
	if err == nil {
		err = c.rw.Flush()
	}
	return n, err
}

func (c *hijackedConn) RemoteAddr() v2net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return c.Conn.RemoteAddr()
}

type httpDuplexConn struct {
	reader     io.ReadCloser
	writer     io.Writer
	flusher    any
	localAddr  v2net.Addr
	remoteAddr v2net.Addr
	doneOnce   sync.Once
	doneCh     chan struct{}
}

func (c *httpDuplexConn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if err != nil {
		c.closeDone()
	}
	return n, err
}

func (c *httpDuplexConn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if f, ok := c.flusher.(http.Flusher); ok {
		f.Flush()
	}
	if err != nil {
		c.closeDone()
	}
	return n, err
}

func (c *httpDuplexConn) Close() error {
	c.closeDone()
	return c.reader.Close()
}

func (c *httpDuplexConn) closeDone() {
	c.doneOnce.Do(func() {
		close(c.doneCh)
	})
}

func (c *httpDuplexConn) LocalAddr() v2net.Addr                { return c.localAddr }
func (c *httpDuplexConn) RemoteAddr() v2net.Addr               { return c.remoteAddr }
func (c *httpDuplexConn) SetDeadline(t time.Time) error      { return nil }
func (c *httpDuplexConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *httpDuplexConn) SetWriteDeadline(t time.Time) error { return nil }

type pipeDuplexConn struct {
	reader     *io.PipeReader
	writer     *io.PipeWriter
	localAddr  v2net.Addr
	remoteAddr v2net.Addr
	closeOnce  sync.Once
	onClose    func()
}

func (c *pipeDuplexConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *pipeDuplexConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *pipeDuplexConn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}
func (c *pipeDuplexConn) LocalAddr() v2net.Addr                { return c.localAddr }
func (c *pipeDuplexConn) RemoteAddr() v2net.Addr               { return c.remoteAddr }
func (c *pipeDuplexConn) SetDeadline(t time.Time) error      { return nil }
func (c *pipeDuplexConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *pipeDuplexConn) SetWriteDeadline(t time.Time) error { return nil }

type wsStreamConn struct {
	ws         *websocket.Conn
	reader     io.Reader
	remoteAddr v2net.Addr
	writeMu    sync.Mutex
}

func newWSStreamConn(ws *websocket.Conn, remote v2net.Addr) *wsStreamConn {
	return &wsStreamConn{ws: ws, remoteAddr: remote}
}

func (c *wsStreamConn) Read(b []byte) (int, error) {
	for {
		if c.reader == nil {
			_, r, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			c.reader = r
		}
		n, err := c.reader.Read(b)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsStreamConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *wsStreamConn) Close() error {
	return c.ws.Close()
}

func (c *wsStreamConn) LocalAddr() v2net.Addr { return c.ws.LocalAddr() }
func (c *wsStreamConn) RemoteAddr() v2net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return c.ws.RemoteAddr()
}
func (c *wsStreamConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}
func (c *wsStreamConn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *wsStreamConn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
