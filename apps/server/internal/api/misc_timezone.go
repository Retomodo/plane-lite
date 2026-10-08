package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"plane-lite/server/internal/httpx"
)

// timezoneLocations is TimezoneEndpoint's (label, zone) list, in source
// order.
var timezoneLocations = [][2]string{
	{"Midway Island", "Pacific/Midway"}, {"American Samoa", "Pacific/Pago_Pago"}, {"Hawaii", "Pacific/Honolulu"},
	{"Aleutian Islands", "America/Adak"}, {"Marquesas Islands", "Pacific/Marquesas"}, {"Alaska", "America/Anchorage"},
	{"Gambier Islands", "Pacific/Gambier"}, {"Pacific Time (US and Canada)", "America/Los_Angeles"},
	{"Baja California", "America/Tijuana"}, {"Mountain Time (US and Canada)", "America/Denver"},
	{"Arizona", "America/Phoenix"}, {"Chihuahua, Mazatlan", "America/Chihuahua"},
	{"Central Time (US and Canada)", "America/Chicago"}, {"Saskatchewan", "America/Regina"},
	{"Guadalajara, Mexico City, Monterrey", "America/Mexico_City"}, {"Tegucigalpa, Honduras", "America/Tegucigalpa"},
	{"Costa Rica", "America/Costa_Rica"}, {"Eastern Time (US and Canada)", "America/New_York"}, {"Lima", "America/Lima"},
	{"Bogota", "America/Bogota"}, {"Quito", "America/Guayaquil"}, {"Chetumal", "America/Cancun"},
	{"Caracas (Old Venezuela Time)", "America/Caracas"}, {"Atlantic Time (Canada)", "America/Halifax"},
	{"Caracas", "America/Caracas"}, {"Santiago", "America/Santiago"}, {"La Paz", "America/La_Paz"},
	{"Manaus", "America/Manaus"}, {"Georgetown", "America/Guyana"}, {"Bermuda", "Atlantic/Bermuda"},
	{"Newfoundland Time (Canada)", "America/St_Johns"}, {"Buenos Aires", "America/Argentina/Buenos_Aires"},
	{"Brasilia", "America/Sao_Paulo"}, {"Greenland", "America/Godthab"}, {"Montevideo", "America/Montevideo"},
	{"Falkland Islands", "Atlantic/Stanley"}, {"South Georgia and the South Sandwich Islands", "Atlantic/South_Georgia"},
	{"Azores", "Atlantic/Azores"}, {"Cape Verde Islands", "Atlantic/Cape_Verde"}, {"Dublin", "Europe/Dublin"},
	{"Reykjavik", "Atlantic/Reykjavik"}, {"Lisbon", "Europe/Lisbon"}, {"Monrovia", "Africa/Monrovia"},
	{"Casablanca", "Africa/Casablanca"}, {"Central European Time (Berlin, Rome, Paris)", "Europe/Paris"},
	{"West Central Africa", "Africa/Lagos"}, {"Algiers", "Africa/Algiers"}, {"Lagos", "Africa/Lagos"},
	{"Tunis", "Africa/Tunis"}, {"Eastern European Time (Cairo, Helsinki, Kyiv)", "Europe/Kyiv"},
	{"Athens", "Europe/Athens"}, {"Jerusalem", "Asia/Jerusalem"}, {"Johannesburg", "Africa/Johannesburg"},
	{"Harare, Pretoria", "Africa/Harare"}, {"Moscow Time", "Europe/Moscow"}, {"Baghdad", "Asia/Baghdad"},
	{"Nairobi", "Africa/Nairobi"}, {"Kuwait, Riyadh", "Asia/Riyadh"}, {"Tehran", "Asia/Tehran"},
	{"Abu Dhabi", "Asia/Dubai"}, {"Baku", "Asia/Baku"}, {"Yerevan", "Asia/Yerevan"}, {"Astrakhan", "Europe/Astrakhan"},
	{"Tbilisi", "Asia/Tbilisi"}, {"Mauritius", "Indian/Mauritius"}, {"Kabul", "Asia/Kabul"},
	{"Islamabad", "Asia/Karachi"}, {"Karachi", "Asia/Karachi"}, {"Tashkent", "Asia/Tashkent"},
	{"Yekaterinburg", "Asia/Yekaterinburg"}, {"Maldives", "Indian/Maldives"}, {"Chagos", "Indian/Chagos"},
	{"Chennai", "Asia/Kolkata"}, {"Kolkata", "Asia/Kolkata"}, {"Mumbai", "Asia/Kolkata"}, {"New Delhi", "Asia/Kolkata"},
	{"Sri Jayawardenepura", "Asia/Colombo"}, {"Kathmandu", "Asia/Kathmandu"}, {"Dhaka", "Asia/Dhaka"},
	{"Almaty", "Asia/Almaty"}, {"Bishkek", "Asia/Bishkek"}, {"Thimphu", "Asia/Thimphu"},
	{"Yangon (Rangoon)", "Asia/Yangon"}, {"Cocos Islands", "Indian/Cocos"}, {"Bangkok", "Asia/Bangkok"},
	{"Hanoi", "Asia/Ho_Chi_Minh"}, {"Jakarta", "Asia/Jakarta"}, {"Novosibirsk", "Asia/Novosibirsk"},
	{"Krasnoyarsk", "Asia/Krasnoyarsk"}, {"Beijing", "Asia/Shanghai"}, {"Singapore", "Asia/Singapore"},
	{"Perth", "Australia/Perth"}, {"Hong Kong", "Asia/Hong_Kong"}, {"Ulaanbaatar", "Asia/Ulaanbaatar"},
	{"Palau", "Pacific/Palau"}, {"Eucla", "Australia/Eucla"}, {"Tokyo", "Asia/Tokyo"}, {"Seoul", "Asia/Seoul"},
	{"Yakutsk", "Asia/Yakutsk"}, {"Adelaide", "Australia/Adelaide"}, {"Darwin", "Australia/Darwin"},
	{"Sydney", "Australia/Sydney"}, {"Brisbane", "Australia/Brisbane"}, {"Guam", "Pacific/Guam"},
	{"Vladivostok", "Asia/Vladivostok"}, {"Tahiti", "Pacific/Tahiti"}, {"Lord Howe Island", "Australia/Lord_Howe"},
	{"Solomon Islands", "Pacific/Guadalcanal"}, {"Magadan", "Asia/Magadan"}, {"Norfolk Island", "Pacific/Norfolk"},
	{"Bougainville Island", "Pacific/Bougainville"}, {"Chokurdakh", "Asia/Srednekolymsk"},
	{"Auckland", "Pacific/Auckland"}, {"Wellington", "Pacific/Auckland"}, {"Fiji Islands", "Pacific/Fiji"},
	{"Anadyr", "Asia/Anadyr"}, {"Chatham Islands", "Pacific/Chatham"}, {"Nuku'alofa", "Pacific/Tongatapu"},
	{"Samoa", "Pacific/Apia"}, {"Kiritimati Island", "Pacific/Kiritimati"},
}

type timezoneEntry struct {
	UTCOffset string `json:"utc_offset"`
	GMTOffset string `json:"gmt_offset"`
	Value     string `json:"value"`
	Label     string `json:"label"`
}

// timezoneList is TimezoneEndpoint.get's list at now: sorted by the %z
// offset read as an integer, then by label. The offset text floors the
// hours, so UTC-09:30 reads "-10:30" as in Django.
func timezoneList(now time.Time) []timezoneEntry {
	type keyed struct {
		offset int
		entry  timezoneEntry
	}
	var list []keyed
	for _, loc := range timezoneLocations {
		tz, err := time.LoadLocation(loc[1])
		if err != nil {
			continue // pytz.exceptions.UnknownTimeZoneError
		}
		_, secs := now.In(tz).Zone()
		z, _ := strconv.Atoi(now.In(tz).Format("-0700"))
		hours := floorDiv(secs, 3600)
		minutes := (secs - hours*3600) / 60
		sign := "+"
		if hours < 0 {
			sign = "-"
		}
		off := fmt.Sprintf("%s%02d:%02d", sign, abs(hours), minutes)
		list = append(list, keyed{z, timezoneEntry{UTCOffset: "UTC" + off, GMTOffset: "GMT" + off, Value: loc[1], Label: loc[0]}})
	}
	slices.SortStableFunc(list, func(a, b keyed) int {
		if a.offset != b.offset {
			return a.offset - b.offset
		}
		return strings.Compare(a.entry.Label, b.entry.Label)
	})
	out := make([]timezoneEntry, len(list))
	for i, k := range list {
		out[i] = k.entry
	}
	return out
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// timezonePageTimeout is cache_page(60 * 60 * 2).
const timezonePageTimeout = 2 * 60 * 60

// listTimezones ports TimezoneEndpoint.get. AuthenticationThrottle runs for
// anonymous clients. Django also stores the page in Redis for two hours;
// Go builds it per request but sends the same Expires and max-age, so
// browsers cache it the same way (DEVIATIONS.md).
func (a *API) listTimezones(c *httpx.Ctx) error {
	if c.User == nil {
		if err := a.throttle(c, "authentication", a.authRate); err != nil {
			return err
		}
	}
	now := time.Now()
	h := c.W.Header()
	h.Set("Expires", now.Add(timezonePageTimeout*time.Second).UTC().Format(http.TimeFormat))
	h.Set("Cache-Control", "max-age="+strconv.Itoa(timezonePageTimeout))
	return c.JSON(http.StatusOK, map[string]any{"timezones": timezoneList(now)})
}

// timezonesNotAllowed is TimezoneEndpoint's http_method_not_allowed, reached
// after the throttle.
func (a *API) timezonesNotAllowed(c *httpx.Ctx) error {
	if c.User == nil {
		if err := a.throttle(c, "authentication", a.authRate); err != nil {
			return err
		}
	}
	c.W.Header().Set("Allow", "GET, HEAD, OPTIONS")
	return httpx.Detail(http.StatusMethodNotAllowed, `Method "`+c.R.Method+`" not allowed.`)
}
