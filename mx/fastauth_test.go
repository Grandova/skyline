package mx

import (
	"context"
	"testing"

	"github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/protocol"
)

func TestBuildFastAuthPayload(t *testing.T) {
	target := net.TCPDestination(net.ParseAddress("example.com"), net.Port(443))
	payload := buildFastAuthPayload("test-token", target)

	if payload.Protocol != "mx" {
		t.Fatalf("protocol = %q, want mx", payload.Protocol)
	}
	if payload.AuthData != "test-token" {
		t.Fatalf("authData = %q, want test-token", payload.AuthData)
	}
	if payload.TargetHost != "example.com" {
		t.Fatalf("targetHost = %q, want example.com", payload.TargetHost)
	}
	if payload.TargetPort != "443" {
		t.Fatalf("targetPort = %q, want 443", payload.TargetPort)
	}
	if payload.Network != "tcp" {
		t.Fatalf("network = %q, want tcp", payload.Network)
	}
	if len(payload.Args) != 1 || payload.Args[0] != "tcp" {
		t.Fatalf("args = %v, want [tcp]", payload.Args)
	}
}

func TestBuildFastAuthPayloadUDP(t *testing.T) {
	target := net.UDPDestination(net.ParseAddress("8.8.8.8"), net.Port(53))
	payload := buildFastAuthPayload("dns-token", target)

	if payload.Network != "udp" {
		t.Fatalf("network = %q, want udp", payload.Network)
	}
	if payload.Args[0] != "udp" {
		t.Fatalf("args[0] = %q, want udp", payload.Args[0])
	}
}

func TestParseFastAuthPayloadTCP(t *testing.T) {
	payload := &FastAuthPayload{
		Protocol:   "mx",
		AuthData:   "test-token",
		TargetHost: "1.1.1.1",
		TargetPort: "443",
		Network:    "tcp",
		Args:       []string{"tcp"},
	}

	target, proto, err := parseFastAuthPayload(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if proto != "tcp" {
		t.Fatalf("protocol = %q, want tcp", proto)
	}
	if target.Network != net.Network_TCP {
		t.Fatalf("target network = %v, want TCP", target.Network)
	}
	if target.Address.String() != "1.1.1.1" {
		t.Fatalf("target address = %s", target.Address)
	}
	if target.Port != net.Port(443) {
		t.Fatalf("target port = %d", target.Port)
	}
}

func TestParseFastAuthPayloadUDP(t *testing.T) {
	payload := &FastAuthPayload{
		Protocol:   "mx",
		AuthData:   "token",
		TargetHost: "8.8.8.8",
		TargetPort: "53",
		Network:    "udp",
		Args:       []string{"udp"},
	}

	target, proto, err := parseFastAuthPayload(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if proto != "udp" {
		t.Fatalf("protocol = %q, want udp", proto)
	}
	if target.Network != net.Network_UDP {
		t.Fatalf("target network = %v, want UDP", target.Network)
	}
}

func TestValidateFastAuth(t *testing.T) {
	validador := &Validador{}
	usuario := &protocol.MemoryUser{
		Account: &CuentaMemoria{Id: "test-token"},
		Level:   1,
	}
	if err := validador.Add(usuario); err != nil {
		t.Fatalf("add user: %v", err)
	}

	servidor := &Servidor{validador: validador}

	payload := &FastAuthPayload{
		Protocol:   "mx",
		AuthData:   "test-token",
		TargetHost: "example.com",
		TargetPort: "443",
		Network:    "tcp",
		Args:       []string{"tcp"},
	}

	inbound, err := servidor.ValidateFastAuth(context.Background(), payload)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if inbound == nil {
		t.Fatal("inbound is nil")
	}
	if inbound.Protocol != "mx" {
		t.Fatalf("protocol = %q, want mx", inbound.Protocol)
	}
	if !inbound.ConsumeClassicHeader {
		t.Fatal("expected ConsumeClassicHeader to be true")
	}
	if user, ok := inbound.User.(*protocol.MemoryUser); !ok || user != usuario {
		t.Fatal("user mismatch")
	}
	if target, ok := inbound.Target.(net.Destination); !ok || target.Address.String() != "example.com" {
		t.Fatal("target mismatch")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload := &FastAuthPayload{
		Version:    FastAuthVersionClassic,
		Protocol:   "mx",
		AuthData:   "token|with\\pipe",
		TargetHost: "example.com",
		TargetPort: "443",
		Network:    "tcp",
		Args:       []string{"tcp"},
	}

	encoded, err := EncodeFastAuthPayload(payload)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	decoded, err := DecodeFastAuthPayload(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	if decoded.AuthData != payload.AuthData {
		t.Fatalf("authData = %q, want %q", decoded.AuthData, payload.AuthData)
	}
}
