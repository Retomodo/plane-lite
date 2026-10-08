package api

import (
	"context"
	"net/netip"
	"testing"
)

func TestIsBlockedIPMatchesPython(t *testing.T) {
	// Verdicts of plane.utils.ip_address.is_blocked_ip on Python 3.12.
	cases := map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true, "169.254.169.254": true,
		"100.64.0.1": true, "0.0.0.0": true, "224.0.0.1": true, "240.0.0.1": true, "255.255.255.255": true,
		"192.0.0.9": false, "8.8.8.8": false, "1.1.1.1": false,
		"::1": true, "::": true, "fe80::1": true, "fc00::1": true, "fec0::1": true, "ff02::1": true,
		"::ffff:8.8.8.8": true, "64:ff9b::808:808": true, "2002:808:808::1": true, "2001::1": true,
		"2001:db8::1": true, "2001:4860:4860::8888": false, "2606:4700:4700::1111": false,
	}
	for s, want := range cases {
		if got := isBlockedIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("isBlockedIP(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestCrawlBlockedURL(t *testing.T) {
	for _, u := range []string{"http://localhost/docs", "http://127.0.0.1:9/x", "ftp://example.com/", "http:///nohost"} {
		meta := crawlTitleAndFavicon(context.Background(), u)
		if meta["title"] != nil || meta["favicon_url"] != nil || meta["url"] != u ||
			meta["favicon"] != "data:image/svg+xml;base64,"+defaultFavicon {
			t.Errorf("crawl(%s) = %v", u, meta)
		}
	}
}
