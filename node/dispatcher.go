package node

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/protobuf/types/known/wrapperspb"

	core "github.com/v2fly/v2ray-core/v5"
	"github.com/v2fly/v2ray-core/v5/app/dispatcher"
	"github.com/v2fly/v2ray-core/v5/common"
	v2net "github.com/v2fly/v2ray-core/v5/common/net"
	"github.com/v2fly/v2ray-core/v5/common/session"
	"github.com/v2fly/v2ray-core/v5/features/outbound"
	"github.com/v2fly/v2ray-core/v5/features/policy"
	"github.com/v2fly/v2ray-core/v5/features/routing"
	"github.com/v2fly/v2ray-core/v5/features/stats"
	"github.com/v2fly/v2ray-core/v5/transport"
)

// SkylineDispatcher wraps v2ray-core's DefaultDispatcher to enforce Xboard device limits,
// per-user speed limits, online IP tracking, and server route rules.
type SkylineDispatcher struct {
	*dispatcher.DefaultDispatcher
}

var registerDispatcherOnce sync.Once

// RegisterSkylineDispatcher registers SkylineDispatcher with v2ray-core's config creator table.
func RegisterSkylineDispatcher() {
	registerDispatcherOnce.Do(func() {
		_ = common.RegisterConfig((*wrapperspb.BoolValue)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
			base := new(dispatcher.DefaultDispatcher)
			if err := core.RequireFeatures(ctx, func(om outbound.Manager, router routing.Router, pm policy.Manager, sm stats.Manager) error {
				return base.Init(&dispatcher.Config{}, om, router, pm, sm)
			}); err != nil {
				return nil, err
			}
			return &SkylineDispatcher{DefaultDispatcher: base}, nil
		})
	})
}

func init() {
	RegisterSkylineDispatcher()
}

// Type implements common.HasType.
func (*SkylineDispatcher) Type() interface{} {
	return routing.DispatcherType()
}

// Dispatch intercepts every proxied stream/packet session after MX authentication.
func (d *SkylineDispatcher) Dispatch(ctx context.Context, destination v2net.Destination) (*transport.Link, error) {
	inbound := session.InboundFromContext(ctx)
	var tracker *NodeTracker
	if inbound != nil && inbound.Tag != "" {
		tracker = GetTracker(inbound.Tag)
	}

	if tracker != nil {
		var protoName string
		if content := session.ContentFromContext(ctx); content != nil {
			protoName = content.Protocol
		}
		action, actionVal := tracker.MatchRoute(destination, protoName)
		switch action {
		case "block":
			return nil, fmt.Errorf("destination %s blocked by node route rule", destination.String())
		case "dns":
			if actionVal != "" && destination.Port == 53 {
				if dnsAddr := v2net.ParseAddress(actionVal); dnsAddr != nil {
					destination.Address = dnsAddr
				}
			}
		case "direct":
			ctx = session.SetForcedOutboundTagToContext(ctx, "direct")
		}

		var email string
		if inbound.User != nil {
			email = inbound.User.Email
		}
		var clientIP string
		if inbound.Source.IsValid() {
			clientIP = inbound.Source.Address.String()
		}

		state, releaseFn, err := tracker.AcquireConnection(email, clientIP)
		if err != nil {
			return nil, err
		}

		link, err := d.DefaultDispatcher.Dispatch(ctx, destination)
		if err != nil {
			if releaseFn != nil {
				releaseFn()
			}
			return nil, err
		}

		return WrapLinkWithTracking(link, state, releaseFn), nil
	}

	return d.DefaultDispatcher.Dispatch(ctx, destination)
}
