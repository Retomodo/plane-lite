package api

import (
	"fmt"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strings"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// offsetPage is plane.utils.paginator's OffsetPaginator as driven by
// BasePaginator.paginate: ?per_page= (default and cap 1000) and
// ?cursor=<value>:<page>:<is_prev>. Handlers fetch limit+1 rows from offset
// (ordered by the requested key, then -created_at) and build the response.
type offsetPage struct {
	limit  int64
	page   *big.Int
	offset int64
}

var (
	errBadPerPage = httpx.Detail(http.StatusBadRequest, "Invalid per_page parameter.")
	errBadCursor  = httpx.Detail(http.StatusBadRequest, "Invalid cursor parameter.")
)

// maxPageLimit is the paginator's MAX_LIMIT.
const maxPageLimit = 1000

// djangoQuery reads request.GET: Get is QueryDict.get (the last value).
type djangoQuery struct{ c *httpx.Ctx }

func (q djangoQuery) Get(name string) string { return q.c.Query(name) }
func (q djangoQuery) Has(name string) bool   { return q.c.HasQuery(name) }

// pageParams is BasePaginator.paginate's ?per_page= and ?cursor= parsing.
type pageParams struct {
	perPage *big.Int
	// value is the cursor's value: an int, or a float when it has a dot
	// (nil for nan).
	value      *big.Float
	valueFloat bool
	page, prev *big.Int
}

func parsePageParams(c *httpx.Ctx) (*pageParams, error) {
	q := djangoQuery{c}
	perPage := big.NewInt(1000)
	if q.Has("per_page") {
		n, ok := drf.PyIntString(q.Get("per_page"))
		if !ok {
			return nil, errBadPerPage
		}
		perPage = n
	}
	if perPage.Cmp(big.NewInt(1000)) > 0 {
		return nil, httpx.Detail(http.StatusBadRequest, "Invalid per_page value. Cannot exceed 1000.")
	}
	raw := perPage.String() + ":0:0"
	if q.Has("cursor") {
		raw = q.Get("cursor")
	}
	bits := strings.Split(raw, ":")
	if len(bits) != 3 {
		return nil, errBadCursor
	}
	p := &pageParams{perPage: perPage}
	// Cursor.from_string: float(value) when it has a dot, else int(value).
	if strings.Contains(bits[0], ".") {
		f, ok := drf.PyFloat(drf.JSONValue(jsonString(bits[0])))
		if !ok {
			return nil, errBadCursor
		}
		p.valueFloat = true
		if !math.IsNaN(f) {
			p.value = big.NewFloat(f)
		}
	} else {
		n, ok := drf.PyIntString(bits[0])
		if !ok {
			return nil, errBadCursor
		}
		p.value = new(big.Float).SetInt(n)
	}
	var ok bool
	if p.page, ok = drf.PyIntString(bits[1]); !ok {
		return nil, errBadCursor
	}
	if p.prev, ok = drf.PyIntString(bits[2]); !ok {
		return nil, errBadCursor
	}
	return p, nil
}

func parseOffsetPage(c *httpx.Ctx) (*offsetPage, error) {
	pp, err := parsePageParams(c)
	if err != nil {
		return nil, err
	}
	perPage, value, page, prev := pp.perPage, pp.value, pp.page, pp.prev

	// get_result
	limit := min(perPage.Int64(), maxPageLimit)
	if !perPage.IsInt64() {
		limit = math.MinInt64 // a huge negative per_page
	}
	offset := new(big.Int).Mul(page, big.NewInt(limit))
	if offset.Sign() < 0 {
		return nil, httpx.Detail(http.StatusBadRequest, "Error in parsing")
	}
	if limit <= 0 {
		return nil, errViewCrash // zero division or negative slicing
	}
	if prev.Sign() != 0 && (value == nil || value.Cmp(new(big.Float).SetInt64(limit)) != 0) {
		return nil, errViewCrash // results[-(limit + 1):] on a queryset
	}
	p := &offsetPage{limit: limit, page: page, offset: math.MaxInt64}
	if offset.IsInt64() {
		p.offset = offset.Int64()
	}
	return p, nil
}

// jsonString quotes s as a JSON string value.
func jsonString(s string) []byte {
	b, _ := httpx.Marshal(s, nil)
	return b
}

// pageResponse is BasePaginator.paginate's response body.
type pageResponse struct {
	GroupedBy       *string `json:"grouped_by"`
	SubGroupedBy    *string `json:"sub_grouped_by"`
	TotalCount      int64   `json:"total_count"`
	NextCursor      string  `json:"next_cursor"`
	PrevCursor      string  `json:"prev_cursor"`
	NextPageResults bool    `json:"next_page_results"`
	PrevPageResults bool    `json:"prev_page_results"`
	Count           int     `json:"count"`
	TotalPages      int64   `json:"total_pages"`
	TotalResults    int64   `json:"total_results"`
	ExtraStats      any     `json:"extra_stats"`
	Results         any     `json:"results"`
}

// response builds the body from the limit+1 rows fetched at p.offset (n is
// how many came back) and the queryset's total count; results must already
// be cut to p.limit.
func (p *offsetPage) response(results any, n int, total int64) *pageResponse {
	next := new(big.Int).Add(p.page, big.NewInt(1))
	prev := new(big.Int).Sub(p.page, big.NewInt(1))
	return &pageResponse{
		TotalCount:      total,
		NextCursor:      fmt.Sprintf("%d:%s:0", p.limit, next),
		PrevCursor:      fmt.Sprintf("%d:%s:1", p.limit, prev),
		NextPageResults: int64(n) > p.limit,
		PrevPageResults: p.page.Sign() > 0,
		Count:           int(min(int64(n), p.limit)),
		TotalPages:      (total + p.limit - 1) / p.limit,
		TotalResults:    total,
		Results:         results,
	}
}

// sanitizeOrderBy is plane.utils.order_queryset.sanitize_order_by: the
// field and direction of ?order_by=, or of def when it is missing or not
// allowed.
func sanitizeOrderBy(value string, allowed []string, def string) (field string, desc bool) {
	parse := func(v string) (string, bool, bool) {
		bare, isDesc := strings.CutPrefix(v, "-")
		if strings.HasPrefix(bare, "-") || !slices.Contains(allowed, bare) {
			return "", false, false
		}
		return bare, isDesc, true
	}
	if f, d, ok := parse(value); ok && value != "" {
		return f, d
	}
	f, d, _ := parse(def)
	return f, d
}
