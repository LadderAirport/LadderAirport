package control

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

// trafficTracker implements adapter.ConnectionTracker and counts
// active connections plus cumulative uplink/downlink bytes for the
// current box instance.
//
// Convention (same as sing-box v2ray stats / clash):
//   - Read from client  → uplink
//   - Write to client   → downlink
type trafficTracker struct {
	uplink   atomic.Int64
	downlink atomic.Int64
	active   atomic.Int64
}

func newTrafficTracker() *trafficTracker {
	return &trafficTracker{}
}

func (t *trafficTracker) Snapshot() (connections, uplink, downlink int64) {
	return t.active.Load(), t.uplink.Load(), t.downlink.Load()
}

func (t *trafficTracker) RoutedConnection(
	_ context.Context,
	conn net.Conn,
	_ adapter.InboundContext,
	_ adapter.Rule,
	_ adapter.Outbound,
) net.Conn {
	t.active.Add(1)
	counted := bufio.NewInt64CounterConn(conn, []*atomic.Int64{&t.uplink}, []*atomic.Int64{&t.downlink})
	return &trackedConn{Conn: counted, t: t}
}

func (t *trafficTracker) RoutedPacketConnection(
	_ context.Context,
	conn N.PacketConn,
	_ adapter.InboundContext,
	_ adapter.Rule,
	_ adapter.Outbound,
) N.PacketConn {
	t.active.Add(1)
	// sing ≥0.7: separate byte counters and packet counters; we only track bytes.
	counted := bufio.NewInt64CounterPacketConn(
		conn,
		[]*atomic.Int64{&t.uplink},
		nil,
		[]*atomic.Int64{&t.downlink},
		nil,
	)
	return &trackedPacketConn{PacketConn: counted, t: t}
}

// RoutedFlow counts TUN/L3 flows. sing-box 1.14 added this method to
// ConnectionTracker; proxy inbounds still use RoutedConnection.
func (t *trafficTracker) RoutedFlow(
	_ context.Context,
	_ adapter.InboundContext,
	_ adapter.Rule,
	_ adapter.Outbound,
) tun.FlowTracker {
	return &trackedFlow{t: t}
}

type trackedConn struct {
	net.Conn
	t      *trafficTracker
	closed atomic.Bool
}

func (c *trackedConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.t.active.Add(-1)
	}
	return c.Conn.Close()
}

type trackedPacketConn struct {
	N.PacketConn
	t      *trafficTracker
	closed atomic.Bool
}

func (c *trackedPacketConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.t.active.Add(-1)
	}
	return c.PacketConn.Close()
}

// UpstreamType implements optional N.ReaderWithUpstream if CounterConn needs unwrapping.
func (c *trackedConn) Upstream() any { return c.Conn }

func (c *trackedPacketConn) Upstream() any { return c.PacketConn }

type trackedFlow struct {
	t      *trafficTracker
	active atomic.Bool
}

func (f *trackedFlow) AttachFlow(tun.FlowHandle) {}

func (f *trackedFlow) CountForward(n int) {
	f.t.uplink.Add(int64(n))
}

func (f *trackedFlow) CountReverse(n int) {
	f.t.downlink.Add(int64(n))
}

func (f *trackedFlow) FlowEstablished() {
	if f.active.CompareAndSwap(false, true) {
		f.t.active.Add(1)
	}
}

func (f *trackedFlow) CloseFlow(tun.FlowCloseReason) {
	if f.active.CompareAndSwap(true, false) {
		f.t.active.Add(-1)
	}
}
