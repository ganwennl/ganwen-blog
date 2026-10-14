package blog

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type geoTestTransport func(*http.Request) (*http.Response, error)

func (f geoTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func geoTestResponse(raw []byte) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}
}
func geoTestJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }
func geoTestIPv4Client(raw []byte, adjust func(map[string]any)) *http.Client {
	assetURL := "https://github.com/metowolf/qqwry.dat/releases/download/20260930/qqwry.dat"
	asset := map[string]any{"name": "qqwry.dat", "size": len(raw), "browser_download_url": assetURL, "digest": "sha256:" + geoSHA(raw)}
	if adjust != nil {
		adjust(asset)
	}
	return &http.Client{CheckRedirect: geoCheckRedirect, Transport: geoTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			return nil, errors.New("credentials leaked")
		}
		switch r.URL.String() {
		case geoIPv4ReleaseAPI:
			return geoTestResponse(geoTestJSON(map[string]any{"tag_name": "20260930", "assets": []any{asset}})), nil
		case assetURL:
			return geoTestResponse(raw), nil
		}
		return nil, errors.New("unexpected URL")
	})}
}
func geoTestInstall(t *testing.T, dir string, raw []byte, meta geoMetadata) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "geoip"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geoip", "qqwry.dat"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "geoip", "qqwry.meta.json"), geoTestJSON(meta), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGeoManagerSafeUpdateLoadAndFailures(t *testing.T) {
	dir := t.TempDir()
	old := geoTestQQWry(t, "荷兰–阿姆斯特丹", "旧运营商")
	geoTestInstall(t, dir, old, geoMetadata{Version: "20260101", UpdatedAt: "2026-01-01T00:00:00Z", Source: geoIPv4Source, SHA256: geoSHA(old)})
	g := NewGeoManager(dir, false)
	if status := g.Status(); !status.Ready || status.Version != "20260101" || status.Records != 1 {
		t.Fatalf("status %+v", status)
	}
	newRaw := geoTestQQWry(t, "中国–江苏–南京", "新运营商")
	g.client = geoTestIPv4Client(newRaw, nil)
	g.now = func() time.Time { return analyticsTestDate }
	status, err := g.Update(context.Background())
	if err != nil || status.Version != "20260930" || status.Source != geoIPv4Source || status.Bytes != int64(len(newRaw)) {
		t.Fatal(status, err)
	}
	if got, ok := g.Lookup(netip.MustParseAddr("8.8.8.8")); !ok || got.Area != "新运营商" {
		t.Fatal("update not published")
	}
	if reopened := NewGeoManager(dir, false).Status(); !reflect.DeepEqual(status, reopened) {
		t.Fatalf("metadata not persisted %+v %+v", status, reopened)
	}
	beforeDB, _ := os.ReadFile(filepath.Join(dir, "geoip", "qqwry.dat"))
	beforeMeta, _ := os.ReadFile(filepath.Join(dir, "geoip", "qqwry.meta.json"))
	for _, scenario := range []string{"sha mismatch", "oversized asset", "arbitrary URL", "malformed database", "http error", "redirect blocked", "destination directory"} {
		t.Run(scenario, func(t *testing.T) {
			g.client = geoTestIPv4Client(newRaw, func(asset map[string]any) {
				switch scenario {
				case "sha mismatch":
					asset["digest"] = "sha256:" + strings.Repeat("0", 64)
				case "oversized asset":
					asset["size"] = geoMaxBytes + 1
				case "arbitrary URL":
					asset["browser_download_url"] = "https://127.0.0.1/private"
				}
			})
			if scenario == "malformed database" {
				g.client = geoTestIPv4Client([]byte(strings.Repeat("x", 32)), nil)
			}
			if scenario == "http error" {
				g.client.Transport = geoTestTransport(func(r *http.Request) (*http.Response, error) {
					res := geoTestResponse([]byte("provider PRIVATE_ERROR_TOKEN"))
					res.StatusCode = 500
					return res, nil
				})
			}
			if scenario == "redirect blocked" {
				g.client.Transport = geoTestTransport(func(r *http.Request) (*http.Response, error) {
					res := geoTestResponse(nil)
					res.StatusCode = 302
					res.Header.Set("Location", "https://127.0.0.1/private")
					return res, nil
				})
			}
			if scenario == "destination directory" {
				meta := filepath.Join(dir, "geoip", "qqwry.meta.json")
				if err := os.Remove(meta); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(meta, 0700); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Remove(meta); _ = os.WriteFile(meta, beforeMeta, 0600) }()
			}
			got, err := g.Update(context.Background())
			if err == nil || got != status || g.Status() != status {
				t.Fatal("failed update replaced active table", got, err)
			}
			db, _ := os.ReadFile(filepath.Join(dir, "geoip", "qqwry.dat"))
			if !bytes.Equal(beforeDB, db) {
				t.Fatal("failed update changed DB")
			}
			if scenario != "destination directory" {
				meta, _ := os.ReadFile(filepath.Join(dir, "geoip", "qqwry.meta.json"))
				if !bytes.Equal(beforeMeta, meta) {
					t.Fatal("failed update changed metadata")
				}
			}
		})
	}
	g.client = geoTestIPv4Client(geoTestQQWry(t, "美国", "atomic-new"), nil)
	g.rename = func(root *os.Root, oldName, newName string) error {
		if newName == "qqwry.dat" && strings.HasSuffix(oldName, ".db") {
			return errors.New("forced second rename failure")
		}
		return root.Rename(oldName, newName)
	}
	if got, err := g.Update(context.Background()); err == nil || got != status {
		t.Fatal("partial install reported success")
	}
	g.rename = nil
	afterDB, _ := os.ReadFile(filepath.Join(dir, "geoip", "qqwry.dat"))
	afterMeta, _ := os.ReadFile(filepath.Join(dir, "geoip", "qqwry.meta.json"))
	if !bytes.Equal(afterDB, beforeDB) || !bytes.Equal(afterMeta, beforeMeta) {
		t.Fatal("second rename failure did not restore both old files")
	}
	files, _ := os.ReadDir(filepath.Join(dir, "geoip"))
	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".geo-") {
			t.Fatal("temporary file leaked", file.Name())
		}
	}
	g.updating.Lock()
	if _, err := g.Update(context.Background()); !errors.Is(err, errGeoBusy) {
		t.Fatal("parallel update accepted")
	}
	g.updating.Unlock()
	g.client = geoTestIPv4Client(newRaw, nil)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := g.Update(cancelled); err == nil || got != status {
		t.Fatal("cancelled update changed active table", got, err)
	}
	// A checksum mismatch cannot mislabel a manually replaced, valid database.
	badMeta := geoMetadata{Version: "20990101", UpdatedAt: "2099-01-01T00:00:00Z", Source: geoIPv4Source, SHA256: strings.Repeat("0", 64)}
	if err := os.WriteFile(filepath.Join(dir, "geoip", "qqwry.meta.json"), geoTestJSON(badMeta), 0600); err != nil {
		t.Fatal(err)
	}
	if got := NewGeoManager(dir, false).Status(); !got.Ready || got.Version == "20990101" || got.Source != "manual" {
		t.Fatal("unmatched metadata trusted", got)
	}
	if got := NewGeoManager(t.TempDir(), false).Status(); got.Ready || got.Error != "" {
		t.Fatal("missing optional DB failed startup")
	}
	if err := os.WriteFile(filepath.Join(dir, "geoip", "qqwry.dat"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := NewGeoManager(dir, false).Status(); got.Ready || got.Error == "" {
		t.Fatal("corrupt optional DB accepted")
	}
}

func geoTestIPv6Client(raw []byte, badSHA bool) *http.Client {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(raw))
	h.Write(raw)
	blobSHA := hex.EncodeToString(h.Sum(nil))
	return &http.Client{CheckRedirect: geoCheckRedirect, Transport: geoTestTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case geoIPv6ContentsAPI:
			return geoTestResponse(geoTestJSON(map[string]any{"type": "file", "sha": blobSHA, "size": len(raw), "path": "src/libs/ipv6wry.db"})), nil
		case "https://api.github.com/repos/itbdw/ip-database/git/blobs/" + blobSHA:
			data := append([]byte(nil), raw...)
			if badSHA {
				data[0] ^= 1
			}
			return geoTestResponse(geoTestJSON(map[string]any{"encoding": "base64", "sha": blobSHA, "size": len(data), "content": base64.StdEncoding.EncodeToString(data)})), nil
		}
		return nil, errors.New("unexpected URL")
	})}
}

func TestGeoIPv6UpdateAPIAndAggregation(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	raw := geoTestIPv6DB(t)
	g := c.app.Analytics.geo6
	g.client = geoTestIPv6Client(raw, false)
	code, body := c.request("POST", "analytics/geoip/update-v6", map[string]any{}, c.server.URL, c.csrf)
	var response struct {
		Status GeoDatabaseStatus `json:"geo_database_v6"`
	}
	if code != 200 || json.Unmarshal(body, &response) != nil || !response.Status.Ready || response.Status.IPv4Only || response.Status.DataVersion != "20261008" || response.Status.Records != 2 {
		t.Fatal("IPv6 update route", code, string(body))
	}
	before := g.Status()
	g.client = geoTestIPv6Client(raw, true)
	if code, _ := c.request("POST", "analytics/geoip/update-v6", map[string]any{}, c.server.URL, c.csrf); code != 502 || g.Status() != before {
		t.Fatal("tampered blob replaced database", code)
	}
	if got := NewGeoManager(c.app.Config.DataDir, true).Status(); got != before {
		t.Fatal("IPv6 metadata not persisted", got, before)
	}
	app, _ := analyticsFixture(t)
	app.Analytics.geo6.table, _ = ParseIPv6GeoDB(raw)
	w := analyticsEventRequest(app, analyticsPayloadFor(1, "/?p=42"), func(r *http.Request) { r.RemoteAddr = "[2606:4700::1111]:9000" })
	if w.Code != 204 {
		t.Fatal("IPv6 beacon", w.Code)
	}
	report := analyticsGetReport(t, app)
	if !reflect.DeepEqual(report.Regions, []AnalyticsDistribution{{"全球", 1}}) || !reflect.DeepEqual(report.Networks, []AnalyticsDistribution{{"Cloudflare, Inc. Anycast网段", 1}}) || report.Countries[0].Label != "unknown" {
		t.Fatal("IPv6 geography invented country", report)
	}
	app.Config.Preview = false
	w = analyticsEventRequest(app, analyticsPayloadFor(2, "/?p=42"), func(r *http.Request) {
		r.Header.Set("Origin", "https://www.ganwen.nl")
		r.Host = "www.ganwen.nl"
		r.RemoteAddr = "127.0.0.1:8000"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Real-IP", "104.16.0.1")
		r.Header.Set("CF-Connecting-IP", "240.1.2.3")
		r.Header.Set("CF-Connecting-IPv6", "2606:4700::1111")
		r.Header.Set("CF-IPCountry", "NL")
	})
	if w.Code != 204 {
		t.Fatal("CF pseudo IPv4 event", w.Code)
	}
	report = analyticsGetReport(t, app)
	if report.Regions[0].PV != 2 || report.Networks[0].PV != 2 || len(report.Countries) != 2 {
		t.Fatal("pseudo IPv4 did not use actual IPv6", report)
	}
}

func TestGeoUpdateAdminAuthorizationAndJSON(t *testing.T) {
	c := adminTestSetup(t, true)
	raw := geoTestQQWry(t, "荷兰", "Vultr")
	var calls int
	c.app.Analytics.geo4.client = &http.Client{Transport: geoTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("PRIVATE_PROVIDER_SECRET")
	})}
	for _, path := range []string{"analytics/geoip/update", "analytics/geoip/update-v6"} {
		if code, _ := c.request("POST", path, map[string]any{}, c.server.URL, c.csrf); code != 401 {
			t.Fatal("anonymous update", code)
		}
	}
	c.login()
	if code, _ := c.request("POST", "analytics/geoip/update", map[string]any{}, "https://evil.example", c.csrf); code != 403 {
		t.Fatal("bad origin", code)
	}
	if code, _ := c.request("POST", "analytics/geoip/update", map[string]any{}, c.server.URL, "wrong"); code != 403 {
		t.Fatal("bad CSRF", code)
	}
	if code, _ := c.request("POST", "analytics/geoip/update", map[string]any{"url": "https://evil.example"}, c.server.URL, c.csrf); code != 400 {
		t.Fatal("arbitrary URL field", code)
	}
	if calls != 0 {
		t.Fatal("unauthorized request fetched database")
	}
	if code, body := c.request("POST", "analytics/geoip/update", map[string]any{}, c.server.URL, c.csrf); code != 502 || bytes.Contains(body, []byte("PRIVATE_PROVIDER_SECRET")) {
		t.Fatal("update failure not sanitized", code, string(body))
	}
	c.app.Analytics.geo4.client = geoTestIPv4Client(raw, nil)
	if code, body := c.request("POST", "analytics/geoip/update", map[string]any{}, c.server.URL, c.csrf); code != 200 || !bytes.Contains(body, []byte(`"geo_database"`)) {
		t.Fatal("authenticated update failed", code, string(body))
	}
	if code, body := c.request("GET", "analytics?days=7", nil, "", ""); code != 200 || !bytes.Contains(body, []byte(`"regions":[]`)) || !bytes.Contains(body, []byte(`"networks":[]`)) || !bytes.Contains(body, []byte(`"geo_database_v6"`)) {
		t.Fatal("empty report contract", code, string(body))
	}
	// Null is not the required empty JSON object.
	req, _ := http.NewRequest("POST", c.server.URL+"/admin/api/analytics/geoip/update", strings.NewReader("null"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.server.URL)
	req.Header.Set("X-CSRF-Token", c.csrf)
	res, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal("null accepted", res.StatusCode)
	}
}

func TestGeoVisitorTrustAndPseudoIPv4(t *testing.T) {
	for _, test := range []struct {
		name, peer, upstream, cf, v6, country, wantIP, wantCountry string
		preview                                                    bool
	}{
		{"trusted CF", "127.0.0.1:8000", "104.16.0.1", "8.8.8.8", "", "NL", "8.8.8.8", "NL", false},
		{"trusted pseudo", "127.0.0.1:8000", "104.16.0.1", "240.1.2.3", "2606:4700:4700::1111", "US", "2606:4700:4700::1111", "US", false},
		{"unused v6 header", "127.0.0.1:8000", "104.16.0.1", "8.8.8.8", "2606:4700:4700::1111", "NL", "8.8.8.8", "NL", false},
		{"spoof CF direct", "1.1.1.1:8000", "104.16.0.1", "240.1.2.3", "2606:4700:4700::1111", "US", "1.1.1.1", "unknown", false},
		{"direct IPv6", "[2606:4700:4700::1111]:8000", "", "8.8.8.8", "", "US", "2606:4700:4700::1111", "unknown", false},
		{"local Nginx direct", "127.0.0.1:8000", "66.42.81.91", "1.1.1.1", "", "US", "66.42.81.91", "unknown", false},
		{"bad CF visitor", "127.0.0.1:8000", "104.16.0.1", "invalid", "", "US", "", "unknown", false},
		{"bad pseudo private", "127.0.0.1:8000", "104.16.0.1", "240.1.2.3", "::1", "US", "", "unknown", false},
		{"pseudo without IPv6", "127.0.0.1:8000", "104.16.0.1", "240.1.2.3", "", "US", "", "unknown", false},
		{"preview spoof", "127.0.0.1:8000", "66.42.81.91", "1.1.1.1", "", "US", "", "unknown", true},
		{"private direct", "192.168.1.1:8000", "", "", "", "", "", "unknown", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://www.ganwen.nl/analytics/event", nil)
			r.RemoteAddr = test.peer
			for name, value := range map[string]string{"X-Real-IP": test.upstream, "CF-Connecting-IP": test.cf, "CF-Connecting-IPv6": test.v6, "CF-IPCountry": test.country} {
				if value != "" {
					r.Header.Set(name, value)
				}
			}
			cfg := Config{Preview: test.preview}
			ip := analyticsVisitorIP(r, cfg)
			actual := ""
			if ip.IsValid() {
				actual = ip.String()
			}
			country, _ := analyticsCloudflare(r, cfg)
			if actual != test.wantIP || country != test.wantCountry {
				t.Fatalf("got %s %s", actual, country)
			}
		})
	}
	for _, raw := range []string{"http://github.com/x", "https://github.com.evil.test/x", "https://localhost/", "https://api.github.com:8443/x", "https://user:pass@github.com/x"} {
		u, _ := url.Parse(raw)
		if geoAllowedURL(u) {
			t.Fatal("unsafe redirect", raw)
		}
	}
	for _, location := range []string{"中国–香港", "中国—香港", "香港"} {
		if geoCountry(location) != "HK" {
			t.Fatal("Hong Kong country", location)
		}
	}
}

func TestGeoAnalyticsDimensionsAndBackup(t *testing.T) {
	app, _ := analyticsFixture(t)
	raw := geoTestQQWry(t, "荷兰–阿姆斯特丹", "运营商完整原文")
	q, err := ParseQQWry(raw)
	if err != nil {
		t.Fatal(err)
	}
	app.Analytics.geo4.table = q
	for i := 1; i <= 2; i++ {
		if w := analyticsEventRequest(app, analyticsPayloadFor(i, "/?p=42"), nil); w.Code != 204 {
			t.Fatal(w.Code)
		}
	}
	report := analyticsGetReport(t, app)
	if !reflect.DeepEqual(report.Regions, []AnalyticsDistribution{{"荷兰–阿姆斯特丹", 2}}) || !reflect.DeepEqual(report.Networks, []AnalyticsDistribution{{"运营商完整原文", 2}}) || report.Countries[0].Label != "NL" {
		t.Fatal("geo aggregation", report)
	}
	b, err := app.Store.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = validateAnalyticsBackup(b); err != nil {
		t.Fatal("geo backup rejected", err)
	}
	encoded, _ := json.Marshal(b)
	if bytes.Contains(encoded, []byte("192.0.2.10")) {
		t.Fatal("visitor IP stored")
	}
	restored, err := OpenStore(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.RestoreDatabase(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, err := restored.AnalyticsReport(context.Background(), analyticsTestDate.Truncate(24*time.Hour), analyticsTestDate.Truncate(24*time.Hour))
	if err != nil || !reflect.DeepEqual(got.Regions, report.Regions) || !reflect.DeepEqual(got.Networks, report.Networks) {
		t.Fatal("geo restore differs", err, got)
	}
	// MySQL VARCHAR(512) stores characters; full Chinese labels remain exportable.
	long := strings.Repeat("省", 512)
	b.Analytics.Aggregates = append(b.Analytics.Aggregates, AnalyticsAggregate{Date: "2026-10-08", Kind: "region", Label: long, PV: 1})
	if err = validateAnalyticsBackup(b); err != nil {
		t.Fatal("valid 512-character region rejected")
	}
	longStore, err := OpenStore(filepath.Join(t.TempDir(), "long-region.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer longStore.Close()
	if err = longStore.RestoreDatabase(context.Background(), b); err != nil {
		t.Fatal("long Chinese region restore", err)
	}
	longBackup, err := longStore.DatabaseSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expectedRows := map[string]AnalyticsAggregate{}
	for _, row := range b.Analytics.Aggregates {
		expectedRows[analyticsAggregateID(row)] = row
	}
	if len(longBackup.Analytics.Aggregates) != len(expectedRows) {
		t.Fatal("long Chinese region roundtrip lost rows")
	}
	for _, row := range longBackup.Analytics.Aggregates {
		if expectedRows[analyticsAggregateID(row)] != row {
			t.Fatal("long Chinese region roundtrip changed data")
		}
	}
	b.Analytics.Aggregates[len(b.Analytics.Aggregates)-1].Label = long + "市"
	if err = validateAnalyticsBackup(b); err == nil {
		t.Fatal("oversized region accepted")
	}
	// A verified Cloudflare country wins over the offline location's prefix.
	app.Config.Preview = false
	w := analyticsEventRequest(app, analyticsPayloadFor(3, "/?p=42"), func(r *http.Request) {
		r.Header.Set("Origin", "https://www.ganwen.nl")
		r.Host = "www.ganwen.nl"
		r.RemoteAddr = "127.0.0.1:8000"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Real-IP", "104.16.0.1")
		r.Header.Set("CF-Connecting-IP", "8.8.8.8")
		r.Header.Set("CF-IPCountry", "US")
	})
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if report := analyticsGetReport(t, app); len(report.Countries) != 2 {
		t.Fatal("CF country overwritten", report.Countries)
	}
}

func TestGeoManagerLookupDuringAtomicUpdate(t *testing.T) {
	g := NewGeoManager(t.TempDir(), false)
	g.client = geoTestIPv4Client(geoTestQQWry(t, "荷兰", "运营商"), nil)
	var wg sync.WaitGroup
	wg.Add(1)
	stop := make(chan struct{})
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				g.Lookup(netip.MustParseAddr("8.8.8.8"))
				g.Status()
			}
		}
	}()
	_, err := g.Update(context.Background())
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeoIPv6DownloadIntegrityWithLocalPublicFixture(t *testing.T) {
	raw, err := os.ReadFile("../../data/geoip/ipv6wry.db")
	if os.IsNotExist(err) {
		t.Skip("optional public IPv6 fixture missing")
	}
	if err != nil {
		t.Fatal(err)
	}
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(raw))
	h.Write(raw)
	blobSHA := hex.EncodeToString(h.Sum(nil))
	g := NewGeoManager(t.TempDir(), true)
	for _, badSHA := range []bool{false, true} {
		g.client = &http.Client{Transport: geoTestTransport(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case geoIPv6ContentsAPI:
				return geoTestResponse(geoTestJSON(map[string]any{"type": "file", "sha": blobSHA, "size": len(raw), "path": "src/libs/ipv6wry.db"})), nil
			case "https://api.github.com/repos/itbdw/ip-database/git/blobs/" + blobSHA:
				data := append([]byte(nil), raw...)
				if badSHA {
					data[0] ^= 1
				}
				return geoTestResponse(geoTestJSON(map[string]any{"encoding": "base64", "sha": blobSHA, "size": len(data), "content": base64.StdEncoding.EncodeToString(data)})), nil
			}
			return nil, errors.New("unexpected URL")
		})}
		status, err := g.Update(context.Background())
		if !badSHA {
			if err != nil || !status.Ready || status.DataVersion != "20200506" || status.Version != "git-"+blobSHA[:12] || status.IPv4Only {
				t.Fatal("IPv6 update", status, err)
			}
		} else {
			if err == nil || !status.Ready || status.DataVersion != "20200506" {
				t.Fatal("IPv6 tampering accepted", status, err)
			}
		}
	}
}

func TestGeoInstalledPublicDatabaseMetadata(t *testing.T) {
	if _, err := os.Stat("../../data/geoip/qqwry.dat"); os.IsNotExist(err) {
		t.Skip("optional installed public databases missing")
	}
	// Read-only public files; no application, database or production settings.
	for _, ipv6 := range []bool{false, true} {
		g := NewGeoManager("../../data", ipv6)
		status := g.Status()
		if !status.Ready || status.Records < 1 || status.Bytes < 1 {
			t.Fatalf("installed database: %+v", status)
		}
		if ipv6 && status.DataVersion != "20200506" {
			t.Fatal("IPv6 source date was concealed", status)
		}
		t.Logf("ipv6=%v version=%s data_version=%s records=%d bytes=%d source=%s", ipv6, status.Version, status.DataVersion, status.Records, status.Bytes, status.Source)
	}
}
