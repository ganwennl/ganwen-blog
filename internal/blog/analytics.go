package blog

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Analytics struct {
	app          *App
	geo4         *GeoManager
	geo6         *GeoManager
	now          func() time.Time
	mu           sync.Mutex
	rateKey      []byte
	limits       map[string]analyticsRate
	minute       time.Time
	global       int
	sidebarMu    sync.Mutex
	sidebarCache publicSidebarCache
}
type analyticsRate struct {
	minute time.Time
	count  int
}
type analyticsPayload struct {
	EventID   string `json:"event_id"`
	VisitorID string `json:"visitor_id"`
	SessionID string `json:"session_id,omitempty"`
	Path      string `json:"path"`
	Language  string `json:"language"`
	Referrer  string `json:"referrer,omitempty"`
}
type AnalyticsTrend struct {
	Date string `json:"date"`
	PV   int64  `json:"pv"`
	UV   int64  `json:"uv"`
}
type AnalyticsDistribution struct {
	Label string `json:"label"`
	PV    int64  `json:"pv"`
}
type AnalyticsPage struct {
	Path   string `json:"path"`
	Title  string `json:"title"`
	PostID int64  `json:"post_id"`
	PV     int64  `json:"pv"`
}
type AnalyticsReport struct {
	From          string                  `json:"from"`
	To            string                  `json:"to"`
	Timezone      string                  `json:"timezone"`
	PV            int64                   `json:"pv"`
	UVDailySum    int64                   `json:"uv_daily_sum"`
	Trend         []AnalyticsTrend        `json:"trend"`
	TopPages      []AnalyticsPage         `json:"top_pages"`
	Sources       []AnalyticsDistribution `json:"sources"`
	Countries     []AnalyticsDistribution `json:"countries"`
	Languages     []AnalyticsDistribution `json:"languages"`
	Devices       []AnalyticsDistribution `json:"devices"`
	Browsers      []AnalyticsDistribution `json:"browsers"`
	OS            []AnalyticsDistribution `json:"os"`
	Regions       []AnalyticsDistribution `json:"regions"`
	Networks      []AnalyticsDistribution `json:"networks"`
	GeoDatabase   GeoDatabaseStatus       `json:"geo_database"`
	GeoDatabaseV6 GeoDatabaseStatus       `json:"geo_database_v6"`
}

var analyticsToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func NewAnalytics(app *App) (*Analytics, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &Analytics{app: app, now: time.Now, rateKey: key, limits: map[string]analyticsRate{}, geo4: NewGeoManager(app.Config.DataDir, false), geo6: NewGeoManager(app.Config.DataDir, true)}, nil
}
func (a *Analytics) rateAllowed(r *http.Request) bool {
	ip := a.app.Admin.auth.loginIP(r)
	if country, client := analyticsCloudflare(r, a.app.Config); country != "unknown" || client != "" {
		ip = client
	}
	key := analyticsHash(a.rateKey, "rate", ip)
	now := a.now().UTC().Truncate(time.Minute)
	a.mu.Lock()
	defer a.mu.Unlock()
	if now != a.minute {
		a.minute, a.global = now, 0
		for hash, value := range a.limits {
			if value.minute != now {
				delete(a.limits, hash)
			}
		}
	}
	value := a.limits[key]
	if value.count >= 120 || a.global >= 2000 || (value.count == 0 && len(a.limits) >= 4096) {
		return false
	}
	value.minute, value.count = now, value.count+1
	a.limits[key] = value
	a.global++
	return true
}
func analyticsBot(ua string) bool {
	ua = strings.ToLower(ua)
	if len(ua) == 0 || len(ua) > 2048 {
		return true
	}
	for _, marker := range []string{"bot", "crawler", "spider", "headless", "curl/", "wget/", "python-requests", "go-http-client", "facebookexternalhit", "slurp", "monitor", "lighthouse"} {
		if strings.Contains(ua, marker) {
			return true
		}
	}
	return false
}
func (a *Analytics) handleEvent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	if len(r.Header.Values("Origin")) != 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1 || !sameOrigin(r, a.app.Config) || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin") {
		w.WriteHeader(403)
		return
	}
	if strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
		w.WriteHeader(415)
		return
	}
	if r.ContentLength > 4096 {
		w.WriteHeader(413)
		return
	}
	if _, ok := a.app.Admin.auth.session(r); ok {
		w.WriteHeader(204)
		return
	}
	if r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1" || analyticsBot(r.UserAgent()) {
		w.WriteHeader(204)
		return
	}
	if !a.rateAllowed(r) {
		w.WriteHeader(429)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var p analyticsPayload
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			w.WriteHeader(413)
		} else {
			w.WriteHeader(400)
		}
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		w.WriteHeader(400)
		return
	}
	if !analyticsToken.MatchString(p.EventID) || !analyticsToken.MatchString(p.VisitorID) || (p.SessionID != "" && !analyticsToken.MatchString(p.SessionID)) {
		w.WriteHeader(400)
		return
	}
	path, lang, postID, err := a.publicPage(r, p.Path)
	if err != nil {
		w.WriteHeader(400)
		return
	}
	if p.Language != "" && p.Language != lang {
		w.WriteHeader(400)
		return
	}
	source := analyticsSource(p.Referrer, r.Host)
	country, _ := analyticsCloudflare(r, a.app.Config)
	region, network := "unknown", "unknown"
	if ip := analyticsVisitorIP(r, a.app.Config); ip.IsValid() {
		manager := a.geo4
		if ip.Is6() {
			manager = a.geo6
		}
		if location, ok := manager.Lookup(ip); ok {
			region, network = analyticsGeoLabel(location.Location), analyticsGeoLabel(location.Area)
			if country == "unknown" {
				country = geoCountry(location.Location)
			}
		}
	}
	device, browser, os := analyticsAgent(r.UserAgent())
	dimensions := []AnalyticsAggregate{{Kind: "page", Label: path, PostID: postID}, {Kind: "source", Label: source}, {Kind: "country", Label: country}, {Kind: "language", Label: lang}, {Kind: "device", Label: device}, {Kind: "browser", Label: browser}, {Kind: "os", Label: os}, {Kind: "region", Label: region}, {Kind: "network", Label: network}}
	if _, err = a.app.Store.RecordAnalytics(r.Context(), a.now(), p.EventID, p.VisitorID, dimensions); err != nil {
		w.WriteHeader(503)
		return
	}
	w.WriteHeader(204)
}
func (a *Analytics) publicPage(r *http.Request, raw string) (string, string, int64, error) {
	invalid := errors.New("invalid public analytics page")
	if len(raw) > 2048 || !strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", "", 0, invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || u.Scheme != "" || u.User != nil || u.Fragment != "" {
		return "", "", 0, invalid
	}
	lang, route, ok := publicLanguagePath(u.Path)
	if !ok || (route != "/" && route != "/index.php") {
		return "", "", 0, invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", "", 0, invalid
	}
	for key, values := range q {
		if len(values) != 1 {
			return "", "", 0, invalid
		}
		switch key {
		case "p", "page_id", "cat", "category_name", "tag", "author", "m", "post_format", "paged", "s":
		default:
			return "", "", 0, invalid
		}
	}
	prefix := publicLanguagePrefix(lang) + "/"
	if q.Has("p") || q.Has("page_id") {
		if q.Has("p") && q.Has("page_id") {
			return "", "", 0, invalid
		}
		key := "p"
		if q.Has("page_id") {
			key = "page_id"
		}
		id, err := strconv.ParseInt(q.Get(key), 10, 64)
		if err != nil || id <= 0 {
			return "", "", 0, invalid
		}
		post, err := a.app.Store.GetPost(r.Context(), id)
		if err != nil || post.Status != "publish" || post.Unlisted || (post.Type != "post" && post.Type != "page") || (key == "page_id" && post.Type != "page") {
			return "", "", 0, invalid
		}
		if _, ok := localizedPost(post, lang); !ok {
			return "", "", 0, invalid
		}
		// IDs are the WordPress permalink contract; unrelated query values are
		// ignored here just as on the actual public article renderer.
		return prefix + "?" + key + "=" + strconv.FormatInt(id, 10), lang, id, nil
	}
	settings, err := a.app.Store.Settings(r.Context())
	if err != nil {
		return "", "", 0, err
	}
	settings = publicSettings(settings, a.app.Config)
	posts, err := a.app.Store.ListPosts(r.Context(), false)
	if err != nil {
		return "", "", 0, err
	}
	filtered, valid := filterPublicPosts(posts, settings, q, lang)
	if !valid {
		return "", "", 0, invalid
	}
	page := 1
	if q.Has("paged") {
		page, err = strconv.Atoi(q.Get("paged"))
		if err != nil || page < 1 {
			return "", "", 0, invalid
		}
	}
	if page > 1 && page > (len(filtered)+settings.PostsPerPage-1)/settings.PostsPerPage {
		return "", "", 0, invalid
	}
	if q.Has("s") {
		q.Set("s", "")
	} // Never retain search terms.
	for _, key := range []string{"cat", "author", "paged"} {
		if q.Has(key) {
			value, _ := strconv.ParseInt(q.Get(key), 10, 64)
			q.Set(key, strconv.FormatInt(value, 10))
		}
	}
	path := prefix
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	if len(path) > 512 {
		return "", "", 0, invalid
	}
	return path, lang, 0, nil
}
func analyticsSource(raw, host string) string {
	if raw == "" {
		return "direct"
	}
	if len(raw) > 2048 {
		return "unknown"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return "unknown"
	}
	name := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if len(name) > 253 || strings.ContainsAny(name, " <>'\"\\\x00\r\n") {
		return "unknown"
	}
	current, _ := url.Parse("http://" + host)
	if current != nil && strings.EqualFold(current.Hostname(), name) {
		return "internal"
	}
	if net.ParseIP(name) != nil {
		return "unknown"
	}
	return name
}
func analyticsAgent(ua string) (device, browser, os string) {
	ua = strings.ToLower(ua)
	device, browser, os = "desktop", "other", "other"
	switch {
	case strings.Contains(ua, "ipad") || strings.Contains(ua, "tablet"):
		device = "tablet"
	case strings.Contains(ua, "mobile") || strings.Contains(ua, "iphone") || strings.Contains(ua, "android"):
		device = "mobile"
	}
	switch {
	case strings.Contains(ua, "edg/") || strings.Contains(ua, "edga/") || strings.Contains(ua, "edgios/"):
		browser = "Edge"
	case strings.Contains(ua, "opr/"):
		browser = "Opera"
	case strings.Contains(ua, "firefox/") || strings.Contains(ua, "fxios/"):
		browser = "Firefox"
	case strings.Contains(ua, "chrome/") || strings.Contains(ua, "crios/"):
		browser = "Chrome"
	case strings.Contains(ua, "safari/"):
		browser = "Safari"
	}
	switch {
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad"):
		os = "iOS"
	case strings.Contains(ua, "android"):
		os = "Android"
	case strings.Contains(ua, "windows"):
		os = "Windows"
	case strings.Contains(ua, "macintosh") || strings.Contains(ua, "mac os"):
		os = "macOS"
	case strings.Contains(ua, "linux"):
		os = "Linux"
	}
	return
}

// Source: Cloudflare's published https://www.cloudflare.com/ips-v4 and ips-v6,
// verified 2026-10-08. Unknown/new ranges fail closed until explicitly updated.
var analyticsCFPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, raw := range strings.Fields("173.245.48.0/20 103.21.244.0/22 103.22.200.0/22 103.31.4.0/22 141.101.64.0/18 108.162.192.0/18 190.93.240.0/20 188.114.96.0/20 197.234.240.0/22 198.41.128.0/17 162.158.0.0/15 104.16.0.0/13 104.24.0.0/14 172.64.0.0/13 131.0.72.0/22 2400:cb00::/32 2606:4700::/32 2803:f800::/32 2405:b500::/32 2405:8100::/32 2a06:98c0::/29 2c0f:f248::/32") {
		out = append(out, netip.MustParsePrefix(raw))
	}
	return out
}()

func analyticsOneHeader(r *http.Request, key string) string {
	values := r.Header.Values(key)
	if len(values) != 1 {
		return ""
	}
	return strings.TrimSpace(values[0])
}
func analyticsCloudflare(r *http.Request, cfg Config) (country, client string) {
	country = "unknown"
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	address, err := netip.ParseAddr(peer)
	if cfg.Preview || err != nil || !address.IsLoopback() {
		return
	}
	upstream, err := netip.ParseAddr(analyticsOneHeader(r, "X-Real-IP"))
	if err != nil {
		return
	}
	trusted := false
	for _, prefix := range analyticsCFPrefixes {
		if prefix.Contains(upstream.Unmap()) {
			trusted = true
			break
		}
	}
	if !trusted {
		return
	}
	visitor, err := netip.ParseAddr(analyticsOneHeader(r, "CF-Connecting-IP"))
	if err != nil {
		return
	}
	visitor = visitor.Unmap()
	// Cloudflare's Pseudo IPv4 overwrite mode retains the actual IPv6 only in
	// CF-Connecting-IPv6. That header is trusted solely behind the verified CF
	// upstream and when the connecting IPv4 is in its documented Class E range.
	if visitor.Is4() && netip.MustParsePrefix("240.0.0.0/4").Contains(visitor) {
		visitor, err = netip.ParseAddr(analyticsOneHeader(r, "CF-Connecting-IPv6"))
		if err != nil || !visitor.Is6() || visitor.Is4In6() {
			return
		}
	}
	if !analyticsPublicIP(visitor) {
		return
	}
	client = visitor.String()
	code := strings.ToUpper(analyticsOneHeader(r, "CF-IPCountry"))
	if len(code) == 2 && code != "XX" && code != "T1" && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z' {
		country = code
	}
	return
}

func (a *Analytics) handleReport(w http.ResponseWriter, r *http.Request) {
	from, to, err := analyticsRange(r.URL.Query(), a.now())
	if err != nil {
		adminError(w, 400, "请选择有效日期，最多查看最近365天")
		return
	}
	report, err := a.app.Store.AnalyticsReport(r.Context(), from, to)
	if err != nil {
		adminError(w, 503, "暂时无法读取访问统计")
		return
	}
	report.GeoDatabase, report.GeoDatabaseV6 = a.geo4.Status(), a.geo6.Status()
	adminJSON(w, 200, report)
}
func analyticsRange(q url.Values, now time.Time) (time.Time, time.Time, error) {
	today := now.UTC().Truncate(24 * time.Hour)
	invalid := errors.New("invalid analytics range")
	for key, values := range q {
		if len(values) != 1 {
			return time.Time{}, time.Time{}, invalid
		}
		switch key {
		case "days", "start", "end", "from", "to":
		default:
			return time.Time{}, time.Time{}, invalid
		}
	}
	if (q.Has("start") && q.Has("from")) || (q.Has("end") && q.Has("to")) {
		return time.Time{}, time.Time{}, invalid
	}
	fromRaw, toRaw := q.Get("start"), q.Get("end")
	if fromRaw == "" {
		fromRaw = q.Get("from")
	}
	if toRaw == "" {
		toRaw = q.Get("to")
	}
	if fromRaw != "" || toRaw != "" {
		from, e1 := time.Parse(time.DateOnly, fromRaw)
		to, e2 := time.Parse(time.DateOnly, toRaw)
		if e1 != nil || e2 != nil || from.After(to) || to.After(today) || from.Before(today.AddDate(0, 0, -364)) || q.Has("days") {
			return time.Time{}, time.Time{}, invalid
		}
		return from, to, nil
	}
	days := 30
	if q.Has("days") {
		var err error
		days, err = strconv.Atoi(q.Get("days"))
		if err != nil || (days != 7 && days != 30 && days != 90) {
			return time.Time{}, time.Time{}, invalid
		}
	}
	return today.AddDate(0, 0, -days+1), today, nil
}
func sortAnalyticsDistribution(values map[string]int64) []AnalyticsDistribution {
	out := make([]AnalyticsDistribution, 0, len(values))
	for label, pv := range values {
		out = append(out, AnalyticsDistribution{label, pv})
	}
	order := func() {
		sort.Slice(out, func(i, j int) bool {
			if out[i].PV == out[j].PV {
				return out[i].Label < out[j].Label
			}
			return out[i].PV > out[j].PV
		})
	}
	order()
	if len(out) > 100 {
		var remaining int64
		for _, row := range out[99:] {
			remaining += row.PV
		}
		out = out[:99]
		merged := false
		for i := range out {
			if out[i].Label == "other" {
				out[i].PV += remaining
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, AnalyticsDistribution{"other", remaining})
		}
		order()
	}
	return out
}
