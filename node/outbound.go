package node

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/v2fly/v2ray-core/v5/common"
	"github.com/v2fly/v2ray-core/v5/common/buf"
	"github.com/v2fly/v2ray-core/v5/common/errors"
	v2net "github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/retry"
	"github.com/v2fly/v2ray-core/v5/common/serial"
	"github.com/v2fly/v2ray-core/v5/common/session"
	"github.com/v2fly/v2ray-core/v5/common/signal"
	"github.com/v2fly/v2ray-core/v5/common/task"
	"github.com/v2fly/v2ray-core/v5/proxy"
	"github.com/v2fly/v2ray-core/v5/proxy/freedom"
	"github.com/v2fly/v2ray-core/v5/transport"
	"github.com/v2fly/v2ray-core/v5/transport/internet"

	"skyline/transportext"
)

// SkylineOutboundSettings configures the direct outbound handler, including
// Proxy Protocol v1/v2 header emission and optional upstream forward address.
type SkylineOutboundSettings struct {
	ProxyProtocol  int    `json:"proxy_protocol,omitempty"`  // 0 = off, 1 = Proxy Protocol v1, 2 = Proxy Protocol v2
	ForwardAddress string `json:"forward_address,omitempty"` // Optional fixed target IP or host:port
}

// EncodeSkylineOutboundSettings encodes SkylineOutboundSettings into a protobuf Any message.
func EncodeSkylineOutboundSettings(s SkylineOutboundSettings) *anypb.Any {
	raw, _ := json.Marshal(s)
	return serial.ToTypedMessage(&wrapperspb.BytesValue{Value: raw})
}

// SkylineOutboundHandler implements proxy.Outbound with support for sending
// Proxy Protocol v1 and v2 headers on outbound TCP connections.
type SkylineOutboundHandler struct {
	settings SkylineOutboundSettings
	fallback proxy.Outbound
}

var registerOutboundOnce sync.Once

// RegisterSkylineOutbound registers SkylineOutboundHandler with v2ray-core's config creator table.
func RegisterSkylineOutbound() {
	registerOutboundOnce.Do(func() {
		_ = common.RegisterConfig((*wrapperspb.BytesValue)(nil), func(ctx context.Context, rawCfg interface{}) (interface{}, error) {
			var settings SkylineOutboundSettings
			if bv, ok := rawCfg.(*wrapperspb.BytesValue); ok && bv != nil && len(bv.Value) > 0 {
				_ = json.Unmarshal(bv.Value, &settings)
			}
			fbObj, err := common.CreateObject(ctx, &freedom.Config{})
			if err != nil {
				return nil, err
			}
			fb, ok := fbObj.(proxy.Outbound)
			if !ok {
				return nil, errors.New("failed to initialize freedom fallback outbound")
			}
			return &SkylineOutboundHandler{
				settings: settings,
				fallback: fb,
			}, nil
		})
	})
}

func init() {
	RegisterSkylineOutbound()
}

// Process implements proxy.Outbound.
func (h *SkylineOutboundHandler) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outboundMeta := session.OutboundFromContext(ctx)
	if outboundMeta == nil || !outboundMeta.Target.IsValid() {
		return errors.New("target not specified")
	}
	destination := outboundMeta.Target

	// Use standard freedom handler for UDP or when neither ProxyProtocol nor ForwardAddress is active
	if destination.Network != v2net.Network_TCP || (h.settings.ProxyProtocol <= 0 && strings.TrimSpace(h.settings.ForwardAddress) == "") {
		return h.fallback.Process(ctx, link, dialer)
	}

	dialDest := resolveForwardDestination(destination, h.settings.ForwardAddress)

	var conn internet.Connection
	err := retry.ExponentialBackoff(2, 100).On(func() error {
		rawConn, dialErr := dialer.Dial(ctx, dialDest)
		if dialErr != nil {
			return dialErr
		}
		conn = rawConn
		return nil
	})
	if err != nil {
		return errors.New("failed to open outbound connection to ", dialDest).Base(err)
	}
	defer conn.Close()

	// Send Proxy Protocol v1 or v2 header if configured
	if h.settings.ProxyProtocol == 1 || h.settings.ProxyProtocol == 2 {
		var srcAddr net.Addr = conn.LocalAddr()
		if inMeta := session.InboundFromContext(ctx); inMeta != nil && inMeta.Source.IsValid() && inMeta.Source.Address.Family().IsIP() {
			srcAddr = &net.TCPAddr{
				IP:   inMeta.Source.Address.IP(),
				Port: int(inMeta.Source.Port),
			}
		}

		var dstAddr net.Addr = conn.RemoteAddr()
		if destination.Address.Family().IsIP() {
			dstAddr = &net.TCPAddr{
				IP:   destination.Address.IP(),
				Port: int(destination.Port),
			}
		}

		if err := transportext.WriteProxyProtocolHeader(conn, h.settings.ProxyProtocol, srcAddr, dstAddr); err != nil {
			return errors.New("failed to write Proxy Protocol header").Base(err)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	timer := signal.CancelAfterInactivity(ctx, cancel, 2*time.Minute)

	requestDone := func() error {
		defer timer.SetTimeout(15 * time.Second)
		return buf.Copy(link.Reader, buf.NewWriter(conn), buf.UpdateActivity(timer))
	}

	responseDone := func() error {
		defer timer.SetTimeout(15 * time.Second)
		return buf.Copy(buf.NewReader(conn), link.Writer, buf.UpdateActivity(timer))
	}

	responseDoneAndCloseWriter := task.OnSuccess(responseDone, task.Close(link.Writer))
	if err := task.Run(ctx, requestDone, responseDoneAndCloseWriter); err != nil {
		common.Interrupt(link.Reader)
		common.Interrupt(link.Writer)
		return errors.New("connection ends").Base(err)
	}
	return nil
}

func resolveForwardDestination(orig v2net.Destination, forwardAddr string) v2net.Destination {
	forwardAddr = strings.TrimSpace(forwardAddr)
	if forwardAddr == "" {
		return orig
	}
	if host, portStr, err := net.SplitHostPort(forwardAddr); err == nil {
		if parsedAddr := v2net.ParseAddress(host); parsedAddr != nil {
			if p, pErr := v2net.PortFromString(portStr); pErr == nil && p > 0 {
				return v2net.Destination{
					Network: orig.Network,
					Address: parsedAddr,
					Port:    p,
				}
			}
		}
	}
	if parsedAddr := v2net.ParseAddress(forwardAddr); parsedAddr != nil {
		return v2net.Destination{
			Network: orig.Network,
			Address: parsedAddr,
			Port:    orig.Port,
		}
	}
	return orig
}
