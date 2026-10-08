package api

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/google/uuid"
	"golang.org/x/net/html"
	"golang.org/x/net/idna"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/jobs"
)

// bgtasks/work_item_link_task.py: crawl a link's page for its title and
// favicon, through the SSRF-guarded fetch of utils/url_security.py.

// crawlLinkJob is crawl_work_item_link_title's arguments. Actor is the
// request user, whom the eagerly run task's save() records as updated_by.
type crawlLinkJob struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Actor string `json:"actor"`
}

func (crawlLinkJob) Kind() string { return "crawl_work_item_link_title" }

func (a *API) enqueueCrawlLink(ctx context.Context, id uuid.UUID, url string, actor uuid.UUID) {
	if err := jobs.Enqueue(ctx, a.jobs, crawlLinkJob{ID: id.String(), URL: url, Actor: actor.String()}); err != nil {
		a.log.Error("enqueue crawl_work_item_link_title", "err", err)
	}
}

// crawlLinkTitle ports crawl_work_item_link_title: the crawl result becomes
// the link's metadata, saved through save() (updated_at, updated_by).
func (a *API) crawlLinkTitle(ctx context.Context, j crawlLinkJob) error {
	meta := crawlTitleAndFavicon(ctx, j.URL)
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(j.ID)
	if err != nil {
		return err
	}
	var actor *uuid.UUID
	if u, err := uuid.Parse(j.Actor); err == nil {
		actor = &u
	}
	tag, err := a.db.Exec(ctx, `UPDATE issue_links SET metadata = $2::jsonb, updated_at = now(), updated_by_id = $3
		WHERE id = $1 AND deleted_at IS NULL`, id, string(raw), actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		a.log.Warn("IssueLink not found", "id", j.ID, "url", j.URL)
	}
	return nil
}

// defaultFavicon is DEFAULT_FAVICON (lucide's link icon).
const defaultFavicon = "PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIyNCIgaGVpZ2h0PSIyNCIgdmlld0JveD0iMCAwIDI0IDI0IiBmaWxsPSJub25lIiBzdHJva2U9ImN1cnJlbnRDb2xvciIgc3Ryb2tlLXdpZHRoPSIyIiBzdHJva2UtbGluZWNhcD0icm91bmQiIHN0cm9rZS1saW5lam9pbj0icm91bmQiIGNsYXNzPSJsdWNpZGUgbHVjaWRlLWxpbmstaWNvbiBsdWNpZGUtbGluayI+PHBhdGggZD0iTTEwIDEzYTUgNSAwIDAgMCA3LjU0LjU0bDMtM2E1IDUgMCAwIDAtNy4wNy03LjA3bC0xLjcyIDEuNzEiLz48cGF0aCBkPSJNMTQgMTFhNSA1IDAgMCAwLTcuNTQtLjU0bC0zIDNhNSA1IDAgMCAwIDcuMDcgNy4wN2wxLjcxLTEuNzEiLz48L3N2Zz4="

const crawlUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

// maxCrawlBody caps how much of a response is read (requests reads it all).
const maxCrawlBody = 10 << 20

// crawlTitleAndFavicon ports crawl_work_item_link_title_and_favicon. A page
// that cannot be fetched (blocked, unresolvable, unreachable) leaves the
// title None; the favicon falls back to the default icon.
func crawlTitleAndFavicon(ctx context.Context, rawURL string) map[string]any {
	var (
		doc   *html.Node
		title any
	)
	finalURL := rawURL
	if resp, final, err := safeGet(ctx, rawURL, time.Second); err == nil {
		finalURL = final
		if n, err := html.Parse(strings.NewReader(string(resp.body))); err == nil {
			doc = n
			if t := findElement(doc, func(n *html.Node) bool { return n.Data == "title" }); t != nil {
				title = drf.PyStrip(nodeText(t))
			}
		}
	}
	favURL, favicon := fetchFavicon(ctx, doc, finalURL)
	return map[string]any{"title": title, "favicon": favicon, "url": rawURL, "favicon_url": favURL}
}

// fetchFavicon ports fetch_and_encode_favicon: the favicon as a data URI,
// or the default icon (and no URL) on any failure.
func fetchFavicon(ctx context.Context, doc *html.Node, base string) (any, string) {
	fallback := "data:image/svg+xml;base64," + defaultFavicon
	favURL, err := findFaviconURL(ctx, doc, base)
	if err != nil || favURL == "" {
		return nil, fallback
	}
	resp, _, err := safeGet(ctx, favURL, time.Second)
	if err != nil {
		return nil, fallback
	}
	contentType := "image/x-icon"
	if vs, ok := resp.header["Content-Type"]; ok && len(vs) > 0 {
		contentType = vs[0]
	}
	return favURL, "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(resp.body)
}

// faviconRels are find_favicon_url's selectors, in order.
var faviconRels = []string{"icon", "shortcut icon", "apple-touch-icon", "apple-touch-icon-precomposed"}

// findFaviconURL ports find_favicon_url: a <link rel> icon's href (which
// must pass the IP check), else /favicon.ico when a HEAD of it answers 200.
func findFaviconURL(ctx context.Context, doc *html.Node, base string) (string, error) {
	if doc != nil {
		for _, rel := range faviconRels {
			tag := findElement(doc, func(n *html.Node) bool {
				r, ok := attr(n, "rel")
				// BeautifulSoup keeps rel as a whitespace-split list,
				// which the selector compares re-joined with spaces.
				return n.Data == "link" && ok && strings.Join(strings.Fields(r), " ") == rel
			})
			if tag == nil {
				continue
			}
			if href, _ := attr(tag, "href"); href != "" {
				abs, err := urlJoin(base, href)
				if err != nil {
					return "", err
				}
				if _, _, err := resolveSafe(ctx, abs); err != nil {
					return "", err
				}
				return abs, nil
			}
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", nil
	}
	fallback := u.Scheme + "://" + u.Host + "/favicon.ico"
	resp, err := pinnedFetch(ctx, http.MethodHead, fallback, 2*time.Second)
	if err != nil || resp.status != http.StatusOK {
		return "", nil
	}
	return fallback, nil
}

func findElement(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, match); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func urlJoin(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// errSSRF stands for the ValueErrors of the URL checks (bad scheme, no
// host, unresolvable, blocked address).
type errSSRF struct{ why string }

func (e errSSRF) Error() string { return e.why }

// resolveSafe is _split_target plus resolve_and_validate: the URL's host
// resolved, with every address checked against is_blocked_ip.
func resolveSafe(ctx context.Context, rawURL string) (*url.URL, []netip.Addr, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, errSSRF{err.Error()}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil, errSSRF{"Invalid URL scheme. Only HTTP and HTTPS are allowed"}
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, nil, errSSRF{"Invalid URL: No hostname found"}
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return nil, nil, errSSRF{"Hostname could not be resolved"}
		}
		found, err := net.DefaultResolver.LookupNetIP(ctx, "ip", ascii)
		if err != nil {
			return nil, nil, errSSRF{"Hostname could not be resolved"}
		}
		addrs = found
	}
	if len(addrs) == 0 {
		return nil, nil, errSSRF{"No IP addresses found for the hostname"}
	}
	var out []netip.Addr
	for _, ip := range addrs {
		ip = ip.Unmap().WithZone("")
		if isBlockedIP(ip) {
			return nil, nil, errSSRF{"Access to private/internal networks is not allowed"}
		}
		out = append(out, ip)
	}
	return u, out, nil
}

type crawlResponse struct {
	status int
	header http.Header
	body   []byte
}

// pinnedFetch is pinned_fetch: one request, no redirects, connected to an
// address that passed the check (no second DNS lookup), with the original
// host kept for the Host header, SNI and certificate checks.
func pinnedFetch(ctx context.Context, method, rawURL string, timeout time.Duration) (*crawlResponse, error) {
	u, addrs, err := resolveSafe(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, ip := range addrs {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
		TLSClientConfig:       &tls.Config{ServerName: u.Hostname()},
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       10 * timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, errSSRF{err.Error()}
	}
	req.Header.Set("User-Agent", crawlUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCrawlBody))
	if err != nil {
		return nil, err
	}
	return &crawlResponse{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// maxCrawlRedirects is MAX_REDIRECTS.
const maxCrawlRedirects = 5

// safeGet is safe_get (pinned_fetch_following_redirects): redirects are
// followed by hand, each hop checked and pinned again. It returns the final
// URL too.
func safeGet(ctx context.Context, rawURL string, timeout time.Duration) (*crawlResponse, string, error) {
	current := rawURL
	for redirects := 0; ; redirects++ {
		resp, err := pinnedFetch(ctx, http.MethodGet, current, timeout)
		if err != nil {
			return nil, "", err
		}
		switch resp.status {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect,
			http.StatusPermanentRedirect:
		default:
			return resp, current, nil
		}
		location := resp.header.Get("Location")
		if location == "" {
			return resp, current, nil
		}
		if redirects >= maxCrawlRedirects {
			return nil, "", fmt.Errorf("Exceeded %d redirects for URL: %s", maxCrawlRedirects, rawURL)
		}
		next, err := urlJoin(current, location)
		if err != nil {
			return nil, "", errors.Join(errSSRF{"bad redirect"}, err)
		}
		current = next
	}
}

func prefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// The networks Python 3.12's ipaddress flags is_private (less its
// exceptions), is_reserved, is_loopback, is_link_local, is_multicast and
// is_unspecified, plus utils/ip_address.py's own deny-list. The IPv6
// transition prefixes whose embedded IPv4 is_blocked_ip also checks are all
// denied outright here, so that recursion adds nothing.
var (
	blockedNets = prefixes(
		// IPv4 is_private
		"0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/29",
		"192.0.0.170/31", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"240.0.0.0/4", "255.255.255.255/32",
		// IPv4 is_multicast; the deny-list's carrier-grade NAT
		"224.0.0.0/4", "100.64.0.0/10",
		// IPv6 is_private
		"::1/128", "::/128", "::ffff:0:0/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32",
		"2002::/16", "3fff::/20", "fc00::/7", "fe80::/10",
		// IPv6 is_reserved, is_multicast; the deny-list
		"::/8", "100::/8", "200::/7", "400::/6", "800::/5", "1000::/4", "4000::/3", "6000::/3", "8000::/3",
		"a000::/3", "c000::/3", "e000::/4", "f000::/5", "f800::/6", "fe00::/9", "ff00::/8",
		"64:ff9b::/96", "2001::/32", "fec0::/10",
	)
	// privateExceptions are is_private's _private_networks_exceptions;
	// none of them falls in another blocked range.
	privateExceptions = prefixes("192.0.0.9/32", "192.0.0.10/32", "2001:1::1/128", "2001:1::2/128", "2001:3::/32",
		"2001:4:112::/48", "2001:20::/28", "2001:30::/28")
)

// isBlockedIP ports utils/ip_address.is_blocked_ip.
func isBlockedIP(ip netip.Addr) bool {
	for _, p := range privateExceptions {
		if p.Contains(ip) {
			return false
		}
	}
	for _, p := range blockedNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
