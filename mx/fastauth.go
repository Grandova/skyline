package mx

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/v2fly/v2ray-core/v5/common/errors"
	"github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
	"github.com/v2fly/v2ray-core/v5/transport"
	"github.com/v2fly/v2ray-core/v5/transport/internet"
)

const (
	FastAuthProtocol       = "mx"
	 fastAuthProtocol      = "mx"
	FastAuthRequestHeader  = "MundoConnect-Auth"
	FastAuthResponseHeader = "MundoConnect-Response"
	FastAuthVersionClassic = uint8(1)
	FastAuthStatusOK       = "ok"
	FastAuthStatusFail     = "fail"
	argPayloadProtocol     = 0
)

type FastAuthCapabilityState uint32

const (
	FastAuthCapabilityUnknown FastAuthCapabilityState = iota
	FastAuthCapabilityUnsupported
	FastAuthCapabilitySupported
)

type FastAuthState struct {
	state atomic.Uint32
}

func (s *FastAuthState) State() FastAuthCapabilityState {
	if s == nil {
		return FastAuthCapabilityUnknown
	}
	return FastAuthCapabilityState(s.state.Load())
}

func (s *FastAuthState) MarkSupported() {
	if s == nil {
		return
	}
	s.state.Store(uint32(FastAuthCapabilitySupported))
}

func (s *FastAuthState) MarkUnsupported() {
	if s == nil {
		return
	}
	s.state.Store(uint32(FastAuthCapabilityUnsupported))
}

type FastAuthPayload struct {
	Version    uint8
	Protocol   string
	AuthData   string
	TargetHost string
	TargetPort string
	Network    string
	Args       []string
}

func (p *FastAuthPayload) Arg(index int) string {
	if p == nil || index < 0 || index >= len(p.Args) {
		return ""
	}
	return p.Args[index]
}

func (p *FastAuthPayload) SetArg(index int, value string) {
	if p == nil || index < 0 {
		return
	}
	if index >= len(p.Args) {
		next := make([]string, index+1)
		copy(next, p.Args)
		p.Args = next
	}
	p.Args[index] = value
}

type FastAuthPrepared struct {
	link  interface{}
	err   error
	ready chan struct{}
}

func NewFastAuthPrepared() *FastAuthPrepared {
	return &FastAuthPrepared{
		ready: make(chan struct{}),
	}
}

func (p *FastAuthPrepared) Complete(link interface{}, err error) {
	if p == nil {
		return
	}
	p.link = link
	p.err = err
	close(p.ready)
}

func (p *FastAuthPrepared) Wait() (interface{}, error) {
	if p == nil {
		return nil, nil
	}
	<-p.ready
	return p.link, p.err
}

type FastAuthInbound struct {
	Protocol             string
	User                 interface{}
	Target               interface{}
	Args                 []string
	Prepared             *FastAuthPrepared
	ConsumeClassicHeader bool
}

func (i *FastAuthInbound) Arg(index int) string {
	if i == nil || index < 0 || index >= len(i.Args) {
		return ""
	}
	return i.Args[index]
}

func (i *FastAuthInbound) SetArg(index int, value string) {
	if i == nil || index < 0 {
		return
	}
	if index >= len(i.Args) {
		next := make([]string, index+1)
		copy(next, i.Args)
		i.Args = next
	}
	i.Args[index] = value
}

type FastAuthValidator interface {
	ValidateFastAuth(context.Context, *FastAuthPayload) (*FastAuthInbound, error)
}

// ResultadoAutenticacionRapida matches M-master/app/proxyman/rapido.go.
type ResultadoAutenticacionRapida struct {
	Protocolo string
	Usuario   *protocol.MemoryUser
	Destino   net.Destination
	Enlace    *transport.Link
}

// SesionAutenticacionRapida matches M-master/app/proxyman/rapido.go.
type SesionAutenticacionRapida struct {
	Protocolo      string
	Usuario        *protocol.MemoryUser
	Destino        net.Destination
	omitirCabecera bool
}

func NuevaSesionAutenticacionRapida(protocolo string, usuario *protocol.MemoryUser, destino net.Destination) *SesionAutenticacionRapida {
	return &SesionAutenticacionRapida{
		Protocolo: protocolo,
		Usuario:   usuario,
		Destino:   destino,
	}
}

func (s *SesionAutenticacionRapida) Activar() {
	s.omitirCabecera = true
}

func (s *SesionAutenticacionRapida) OmiteCabecera() bool {
	return s != nil && s.omitirCabecera
}

type claveResultadoAutenticacionRapida struct{}
type claveSesionAutenticacionRapida struct{}

func ContextWithResultadoAutenticacionRapida(ctx context.Context, resultado *ResultadoAutenticacionRapida) context.Context {
	return context.WithValue(ctx, claveResultadoAutenticacionRapida{}, resultado)
}

func ResultadoAutenticacionRapidaFromContext(ctx context.Context) *ResultadoAutenticacionRapida {
	valor := ctx.Value(claveResultadoAutenticacionRapida{})
	if valor == nil {
		return nil
	}
	return valor.(*ResultadoAutenticacionRapida)
}

func ContextWithSesionAutenticacionRapida(ctx context.Context, sesion *SesionAutenticacionRapida) context.Context {
	return context.WithValue(ctx, claveSesionAutenticacionRapida{}, sesion)
}

func SesionAutenticacionRapidaFromContext(ctx context.Context) *SesionAutenticacionRapida {
	valor := ctx.Value(claveSesionAutenticacionRapida{})
	if valor == nil {
		return nil
	}
	return valor.(*SesionAutenticacionRapida)
}

type FastAuthResult struct {
	Status string
}

type FastAuthConnWrapper struct {
	internet.Connection
	FastAuthProtocol string
	FastAuthToken    string
	FastAuthUser     interface{}
	FastAuthTarget   net.Destination
	FastAuthResult   *FastAuthResult
}

func (w *FastAuthConnWrapper) Close() error {
	return w.Connection.Close()
}

// UnwrapFastAuth extracts a *FastAuthConnWrapper from a raw or StatCouterConnection-wrapped connection.
func UnwrapFastAuth(conn internet.Connection) *FastAuthConnWrapper {
	if conn == nil {
		return nil
	}
	if w, ok := conn.(*FastAuthConnWrapper); ok {
		return w
	}
	if sc, ok := conn.(*internet.StatCouterConnection); ok && sc != nil {
		if w, ok := sc.Connection.(*FastAuthConnWrapper); ok {
			return w
		}
	}
	return nil
}

type fastAuthPayloadKey struct{}
type fastAuthValidatorKey struct{}
type fastAuthInboundKey struct{}
type fastAuthStateKey struct{}

func ContextWithFastAuthPayload(ctx context.Context, payload *FastAuthPayload) context.Context {
	if payload == nil {
		return ctx
	}
	return context.WithValue(ctx, fastAuthPayloadKey{}, payload)
}

func FastAuthPayloadFromContext(ctx context.Context) *FastAuthPayload {
	payload, _ := ctx.Value(fastAuthPayloadKey{}).(*FastAuthPayload)
	return payload
}

func ContextWithFastAuthValidator(ctx context.Context, validator FastAuthValidator) context.Context {
	if validator == nil {
		return ctx
	}
	return context.WithValue(ctx, fastAuthValidatorKey{}, validator)
}

func FastAuthValidatorFromContext(ctx context.Context) FastAuthValidator {
	validator, _ := ctx.Value(fastAuthValidatorKey{}).(FastAuthValidator)
	return validator
}

func ContextWithFastAuthInbound(ctx context.Context, inbound *FastAuthInbound) context.Context {
	if inbound == nil {
		return ctx
	}
	return context.WithValue(ctx, fastAuthInboundKey{}, inbound)
}

func FastAuthInboundFromContext(ctx context.Context) *FastAuthInbound {
	inbound, _ := ctx.Value(fastAuthInboundKey{}).(*FastAuthInbound)
	return inbound
}

func ContextWithFastAuthState(ctx context.Context, state *FastAuthState) context.Context {
	if state == nil {
		return ctx
	}
	return context.WithValue(ctx, fastAuthStateKey{}, state)
}

func FastAuthStateFromContext(ctx context.Context) *FastAuthState {
	state, _ := ctx.Value(fastAuthStateKey{}).(*FastAuthState)
	return state
}

type FastAuthRequest struct {
	Payload *FastAuthPayload
	state   *FastAuthState
	encoded string
}

func (r FastAuthRequest) Enabled() bool {
	return r.Payload != nil && (r.state == nil || r.state.State() != FastAuthCapabilityUnsupported)
}

func (r FastAuthRequest) EncodedPayload() (string, error) {
	if !r.Enabled() {
		return "", nil
	}
	if r.encoded != "" {
		return r.encoded, nil
	}
	return EncodeFastAuthPayload(r.Payload)
}

func (r FastAuthRequest) SetHTTPHeaders(header http.Header) error {
	if header == nil || !r.Enabled() {
		return nil
	}
	encoded, err := r.EncodedPayload()
	if err != nil {
		return err
	}
	if encoded != "" {
		header.Set(FastAuthRequestHeader, encoded)
	}
	return nil
}

func (r FastAuthRequest) ObserveHeader(header http.Header) *FastAuthResult {
	if header == nil {
		return r.ObserveValue("")
	}
	return r.ObserveValue(header.Get(FastAuthResponseHeader))
}

func (r FastAuthRequest) ObserveValue(value string) *FastAuthResult {
	if value == "" {
		if r.state != nil && r.state.State() == FastAuthCapabilityUnknown {
			r.state.MarkUnsupported()
		}
		return nil
	}
	switch value {
	case FastAuthStatusOK:
		if r.state != nil {
			r.state.MarkSupported()
		}
		return &FastAuthResult{Status: FastAuthStatusOK}
	case FastAuthStatusFail:
		if r.state != nil {
			r.state.MarkUnsupported()
		}
		return &FastAuthResult{Status: FastAuthStatusFail}
	}
	if r.state != nil && r.state.State() == FastAuthCapabilityUnknown {
		r.state.MarkUnsupported()
	}
	return nil
}

func (r FastAuthRequest) ConnectionResult(observed *FastAuthResult) *FastAuthResult {
	if observed != nil {
		return observed
	}
	if r.Enabled() && r.state != nil && r.state.State() == FastAuthCapabilitySupported {
		return &FastAuthResult{Status: FastAuthStatusOK}
	}
	return nil
}

func PrepareFastAuthRequest(ctx context.Context) FastAuthRequest {
	payload := FastAuthPayloadFromContext(ctx)
	if payload == nil {
		return FastAuthRequest{}
	}
	state := FastAuthStateFromContext(ctx)
	if state != nil && state.State() == FastAuthCapabilityUnsupported {
		return FastAuthRequest{}
	}
	encoded, _ := EncodeFastAuthPayload(payload)
	return FastAuthRequest{
		Payload: payload,
		state:   state,
		encoded: encoded,
	}
}

func EncodeFastAuthPayload(payload *FastAuthPayload) (string, error) {
	if payload == nil {
		return "", nil
	}
	var builder strings.Builder
	builder.Grow(6 + len(payload.Protocol) + len(payload.AuthData) + len(payload.Network) + len(payload.TargetHost) + len(payload.TargetPort))
	if payload.Version == 0 {
		payload.Version = FastAuthVersionClassic
	}
	builder.WriteByte('1')
	writeNextFastAuthField(&builder, payload.Protocol)
	writeNextFastAuthField(&builder, payload.AuthData)
	writeNextFastAuthField(&builder, payload.Network)
	writeNextFastAuthField(&builder, payload.TargetHost)
	writeNextFastAuthField(&builder, payload.TargetPort)
	for _, arg := range payload.Args {
		writeNextFastAuthField(&builder, arg)
	}
	return builder.String(), nil
}

func DecodeFastAuthPayload(raw string) (*FastAuthPayload, error) {
	if raw == "" {
		return nil, nil
	}
	fields, err := splitFastAuthFields(raw)
	if err != nil {
		return nil, err
	}
	if len(fields) < 6 {
		return nil, stderrors.New("invalid fast auth payload")
	}
	payload := &FastAuthPayload{
		Protocol:   fields[1],
		AuthData:   fields[2],
		Network:    fields[3],
		TargetHost: fields[4],
		TargetPort: fields[5],
	}
	switch fields[0] {
	case "1":
		payload.Version = FastAuthVersionClassic
	default:
		return nil, stderrors.New("invalid fast auth version: " + fields[0])
	}
	if len(fields) > 6 {
		payload.Args = fields[6:]
	}
	return payload, nil
}

func writeNextFastAuthField(builder *strings.Builder, value string) {
	builder.WriteByte('|')
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\', '|':
			builder.WriteByte('\\')
		}
		builder.WriteByte(value[i])
	}
}

func splitFastAuthFields(raw string) ([]string, error) {
	if !strings.Contains(raw, "\\") {
		return strings.Split(raw, "|"), nil
	}
	fields := make([]string, 0, strings.Count(raw, "|")+1)
	var builder strings.Builder
	escaped := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if escaped {
			builder.WriteByte(ch)
			escaped = false
			continue
		}
		switch ch {
		case '\\':
			escaped = true
		case '|':
			fields = append(fields, builder.String())
			builder.Reset()
		default:
			builder.WriteByte(ch)
		}
	}
	if escaped {
		return nil, stderrors.New("invalid fast auth escape")
	}
	fields = append(fields, builder.String())
	return fields, nil
}

var (
	fastAuthStates   = make(map[string]*FastAuthState)
	fastAuthStatesMu sync.RWMutex

	activeServersMu sync.RWMutex
	activeServers   = make(map[*Servidor]struct{})
)

func registerServer(s *Servidor) {
	if s == nil {
		return
	}
	activeServersMu.Lock()
	activeServers[s] = struct{}{}
	activeServersMu.Unlock()
}

func unregisterServer(s *Servidor) {
	if s == nil {
		return
	}
	activeServersMu.Lock()
	delete(activeServers, s)
	activeServersMu.Unlock()
}

// ValidateFastAuthGlobal validates a FastAuth payload against context validator or active MX server instances.
func ValidateFastAuthGlobal(ctx context.Context, payload *FastAuthPayload) (*FastAuthInbound, error) {
	if v := FastAuthValidatorFromContext(ctx); v != nil {
		return v.ValidateFastAuth(ctx, payload)
	}

	activeServersMu.RLock()
	servers := make([]*Servidor, 0, len(activeServers))
	for s := range activeServers {
		servers = append(servers, s)
	}
	activeServersMu.RUnlock()

	var lastErr error
	for _, s := range servers {
		res, err := s.ValidateFastAuth(ctx, payload)
		if err == nil && res != nil {
			return res, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("mx: no active validator matched fast auth")
}

func proxyFastAuthState(token string, dest net.Destination) *FastAuthState {
	key := token + "@" + dest.NetAddr()
	fastAuthStatesMu.RLock()
	state, ok := fastAuthStates[key]
	fastAuthStatesMu.RUnlock()
	if ok {
		return state
	}

	fastAuthStatesMu.Lock()
	defer fastAuthStatesMu.Unlock()
	if state, ok = fastAuthStates[key]; ok {
		return state
	}
	state = &FastAuthState{}
	fastAuthStates[key] = state
	return state
}

func observeFastAuthResult(conn internet.Connection, state *FastAuthState) {
	if state == nil {
		return
	}
	if wrapper := UnwrapFastAuth(conn); wrapper != nil {
		if wrapper.FastAuthResult != nil && wrapper.FastAuthResult.Status == FastAuthStatusOK {
			state.MarkSupported()
		} else {
			state.MarkUnsupported()
		}
	} else {
		state.MarkUnsupported()
	}
}

func buildFastAuthPayload(token string, target net.Destination) *FastAuthPayload {
	return &FastAuthPayload{
		Version:    FastAuthVersionClassic,
		Protocol:   fastAuthProtocol,
		AuthData:   token,
		TargetHost: target.Address.String(),
		TargetPort: target.Port.String(),
		Network:    target.Network.SystemString(),
		Args:       []string{target.Network.SystemString()},
	}
}

func parseFastAuthPayload(payload *FastAuthPayload) (net.Destination, string, error) {
	if payload == nil || payload.Protocol != fastAuthProtocol {
		return net.Destination{}, "", errors.New("mx: invalid fast auth protocol")
	}
	if payload.TargetHost == "" || payload.TargetPort == "" {
		return net.Destination{}, "", errors.New("mx: missing fast auth target")
	}

	payloadProtocol := payload.Arg(argPayloadProtocol)
	if payloadProtocol == "" {
		payloadProtocol = payload.Network
	}

	port, err := net.PortFromString(payload.TargetPort)
	if err != nil {
		return net.Destination{}, "", errors.New("mx: invalid fast auth port").Base(err)
	}

	address := net.ParseAddress(payload.TargetHost)
	switch payloadProtocol {
	case "tcp":
		return net.TCPDestination(address, port), payloadProtocol, nil
	case "udp":
		return net.UDPDestination(address, port), payloadProtocol, nil
	default:
		return net.Destination{}, "", errors.New("mx: unsupported payload protocol: ", payloadProtocol)
	}
}

func (s *Servidor) ValidateFastAuth(_ context.Context, payload *FastAuthPayload) (*FastAuthInbound, error) {
	target, payloadProtocol, err := parseFastAuthPayload(payload)
	if err != nil {
		return nil, err
	}

	token := limpiarId(payload.AuthData)
	if token == "" {
		return nil, errors.New("mx: empty fast auth token")
	}

	user := s.validador.Buscar(token)
	if user == nil {
		return nil, errors.New("mx: invalid fast auth user")
	}

	return &FastAuthInbound{
		Protocol:             fastAuthProtocol,
		User:                 user,
		Target:               target,
		Args:                 []string{payloadProtocol},
		ConsumeClassicHeader: true,
	}, nil
}
