package blog

import (
	"net"
	"net/http"
	"net/netip"
)

var geoClassE = netip.MustParsePrefix("240.0.0.0/4")

func analyticsPublicIP(address netip.Addr) bool {
	return address.IsValid() && address.Zone() == "" && address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !geoClassE.Contains(address.Unmap())
}

// Production accepts X-Real-IP only from the local Nginx hop configured to
// overwrite it with $remote_addr. Direct requests never trust forwarding headers.
func analyticsVisitorIP(r *http.Request, cfg Config) netip.Addr {
	if _, client := analyticsCloudflare(r, cfg); client != "" {
		address, _ := netip.ParseAddr(client)
		return address.Unmap()
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	address, err := netip.ParseAddr(peer)
	if err != nil {
		return netip.Addr{}
	}
	address = address.Unmap()
	if address.IsLoopback() {
		if cfg.Preview {
			return netip.Addr{}
		}
		upstream, err := netip.ParseAddr(analyticsOneHeader(r, "X-Real-IP"))
		if err != nil {
			return netip.Addr{}
		}
		upstream = upstream.Unmap()
		for _, prefix := range analyticsCFPrefixes {
			if prefix.Contains(upstream) {
				return netip.Addr{}
			}
		} // malformed CF visitor must not become an edge-IP location
		address = upstream
	}
	if !analyticsPublicIP(address) {
		return netip.Addr{}
	}
	return address
}
