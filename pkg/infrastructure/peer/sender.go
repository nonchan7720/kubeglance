package peer

import (
	"context"
	"net/netip"
)

// Sender reports whether this replica (self: its pod IP) is the one that
// sends alert notifications, and how many other Ready replicas there are.
// The sender is the Ready replica with the lowest address: every replica
// reads the same headless Service, so they agree without talking to each
// other or writing anything to the API server. A replica that is not in the
// Service yet counts itself in. Without a headless Service (one replica), or
// without its own address, a replica is the sender.
func Sender(ctx context.Context, r Resolver, host, self string) (sender bool, peers int, err error) {
	addrs, err := lookup(ctx, r, host)
	if err != nil {
		return false, 0, err
	}
	me, known := netip.ParseAddr(self)
	sender = true
	for _, a := range addrs {
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if known == nil && ip == me {
			continue
		}
		peers++
		if known == nil && ip.Less(me) {
			sender = false
		}
	}
	return sender, peers, nil
}
