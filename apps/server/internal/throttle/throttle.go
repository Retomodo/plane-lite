// Package throttle reproduces DRF's SimpleRateThrottle (a sliding window of
// request timestamps per scope and client) on Redis.
package throttle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rate is "N requests per Period", parsed from DRF syntax like "10/minute".
type Rate struct {
	N      int
	Period time.Duration
}

// ParseRate accepts DRF rate strings; only the first letter of the period
// counts, as in DRF ("10/min" == "10/minute").
func ParseRate(s string) (Rate, error) {
	num, period, ok := strings.Cut(s, "/")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || period == "" {
		return Rate{}, fmt.Errorf("throttle: bad rate %q", s)
	}
	d := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour}[period[0]]
	if d == 0 {
		return Rate{}, fmt.Errorf("throttle: bad period in %q", s)
	}
	return Rate{N: n, Period: d}, nil
}

func MustRate(s string) Rate {
	r, err := ParseRate(s)
	if err != nil {
		panic(err)
	}
	return r
}

type Limiter struct {
	rdb    *redis.Client
	prefix string
}

func New(rdb *redis.Client, prefix string) *Limiter { return &Limiter{rdb: rdb, prefix: prefix} }

// Prune expired entries, refuse when full (reporting the oldest entry),
// else record this request.
var allowScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', tonumber(ARGV[1]) - tonumber(ARGV[2]))
local n = redis.call('ZCARD', KEYS[1])
if n >= tonumber(ARGV[3]) then
  local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
  return {0, n, tonumber(oldest[2])}
end
redis.call('ZADD', KEYS[1], ARGV[1], ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return {1, n + 1, 0}
`)

// Allow records a request for (scope, ident) and reports whether it is
// within rate. When refused, wait is DRF's suggested Retry-After.
func (l *Limiter) Allow(ctx context.Context, scope, ident string, rate Rate) (ok bool, wait time.Duration, err error) {
	now := time.Now().UnixMilli()
	window := rate.Period.Milliseconds()
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	key := l.prefix + "throttle_" + scope + "_" + ident
	res, err := allowScript.Run(ctx, l.rdb, []string{key}, now, window, rate.N, strconv.FormatInt(now, 10)+"-"+hex.EncodeToString(nonce[:])).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	if res[0] == 1 {
		return true, 0, nil
	}
	remaining := time.Duration(window-(now-res[2])) * time.Millisecond
	available := rate.N - int(res[1]) + 1
	if available <= 0 {
		return false, 0, nil
	}
	return false, remaining / time.Duration(available), nil
}

// RetryAfter formats wait like DRF's Retry-After header.
func RetryAfter(wait time.Duration) string {
	return strconv.Itoa(int(math.Ceil(wait.Seconds())))
}

// Ident mirrors DRF's get_ident with NUM_PROXIES unset: the whole
// X-Forwarded-For value with whitespace removed, else the peer address.
func Ident(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.Join(strings.Fields(xff), "")
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
