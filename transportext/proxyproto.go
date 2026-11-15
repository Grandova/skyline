package transportext

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/pires/go-proxyproto"
)

// ParseProxyProtocolVersion parses a Proxy Protocol version string or number.
// Accepted values:
//   - 0 / "0" / "off" / "none" / "false" / "" -> 0 (disabled)
//   - 1 / "1" / "v1" / "V1"                   -> 1 (Proxy Protocol v1 text header)
//   - 2 / "2" / "v2" / "V2"                   -> 2 (Proxy Protocol v2 binary header)
func ParseProxyProtocolVersion(raw string) (int, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "0", "off", "none", "false", "disable", "disabled":
		return 0, nil
	case "1", "v1":
		return 1, nil
	case "2", "v2":
		return 2, nil
	default:
		if n, err := strconv.Atoi(s); err == nil && (n == 0 || n == 1 || n == 2) {
			return n, nil
		}
		return 0, fmt.Errorf("invalid proxy_protocol version %q (supported: 0, 1, 2, v1, v2)", raw)
	}
}

// FormatProxyProtocolVersion returns a human-readable string for a Proxy Protocol version (0, 1, or 2).
func FormatProxyProtocolVersion(version int) string {
	switch version {
	case 1:
		return "v1"
	case 2:
		return "v2"
	default:
		return "off"
	}
}

// WriteProxyProtocolHeader writes a Proxy Protocol v1 (version == 1) or v2 (version == 2) header
// to w carrying the given source and destination addresses.
// If version is 0, it is a no-op and returns nil.
func WriteProxyProtocolHeader(w io.Writer, version int, srcAddr net.Addr, dstAddr net.Addr) error {
	if version == 0 {
		return nil
	}
	if version != 1 && version != 2 {
		return fmt.Errorf("unsupported proxy protocol version %d (must be 1 or 2)", version)
	}

	sIP, sPort := extractIPAndPort(srcAddr)
	dIP, dPort := extractIPAndPort(dstAddr)

	if len(sIP) == 0 {
		sIP = net.IPv4(127, 0, 0, 1)
	}
	if len(dIP) == 0 {
		if sIP.To4() == nil {
			dIP = net.IPv6loopback
		} else {
			dIP = net.IPv4(127, 0, 0, 1)
		}
	}

	// Ensure both addresses share the same IP family (IPv4 or IPv6) as required by PROXY protocol spec
	sV4 := sIP.To4()
	dV4 := dIP.To4()
	if sV4 != nil && dV4 != nil {
		sIP = sV4
		dIP = dV4
	} else {
		sIP = sIP.To16()
		dIP = dIP.To16()
	}

	srcTCP := &net.TCPAddr{IP: sIP, Port: sPort}
	dstTCP := &net.TCPAddr{IP: dIP, Port: dPort}

	hdr := proxyproto.HeaderProxyFromAddrs(byte(version), srcTCP, dstTCP)
	_, err := hdr.WriteTo(w)
	return err
}

// WrapProxyProtocolListener wraps a net.Listener with go-proxyproto so it transparently accepts
// and parses both Proxy Protocol v1 and Proxy Protocol v2 headers on incoming connections.
func WrapProxyProtocolListener(ln net.Listener, accept bool) net.Listener {
	if !accept || ln == nil {
		return ln
	}
	if _, already := ln.(*proxyproto.Listener); already {
		return ln
	}
	return &proxyproto.Listener{
		Listener:          ln,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func extractIPAndPort(addr net.Addr) (net.IP, int) {
	if addr == nil {
		return nil, 0
	}
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.IP, a.Port
	case *net.UDPAddr:
		return a.IP, a.Port
	default:
		host, portStr, err := net.SplitHostPort(addr.String())
		if err != nil {
			return net.ParseIP(strings.TrimSpace(addr.String())), 0
		}
		p, _ := strconv.Atoi(portStr)
		return net.ParseIP(strings.TrimSpace(host)), p
	}
}
