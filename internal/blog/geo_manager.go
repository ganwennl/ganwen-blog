package blog

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const geoIPv4Source = "https://github.com/metowolf/qqwry.dat"
const geoIPv6Source = "https://github.com/itbdw/ip-database"
const geoIPv4ReleaseAPI = "https://api.github.com/repos/metowolf/qqwry.dat/releases/latest"
const geoIPv6ContentsAPI = "https://api.github.com/repos/itbdw/ip-database/contents/src/libs/ipv6wry.db?ref=3.x"
const geoV6MaxBytes = 8 << 20

var errGeoBusy = errors.New("geo database update in progress")
var errGeoUpdate = errors.New("geo database update failed")
var geoVersionPattern = regexp.MustCompile(`^(?:[0-9]{8}|git-[0-9a-f]{12,40})$`)

type geoLookup interface {
	Lookup(netip.Addr) (QQWryLocation, bool)
	Records() int
	Version() string
}

type GeoDatabaseStatus struct {
	Ready       bool   `json:"ready"`
	Version     string `json:"version"`
	DataVersion string `json:"data_version"`
	UpdatedAt   string `json:"updated_at"`
	Records     int    `json:"records"`
	Bytes       int64  `json:"bytes"`
	Source      string `json:"source"`
	IPv4Only    bool   `json:"ipv4_only"`
	SHA256      string `json:"sha256,omitempty"`
	Error       string `json:"error,omitempty"`
}
type geoMetadata struct {
	Version     string `json:"version"`
	UpdatedAt   string `json:"updated_at"`
	Records     int    `json:"records,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256"`
	GitBlobSHA1 string `json:"git_blob_sha1,omitempty"`
}

// GeoManager publishes immutable, fully validated tables. Missing or corrupt
// optional databases do not prevent startup or anonymous statistics collection.
type GeoManager struct {
	mu       sync.RWMutex
	updating sync.Mutex
	table    geoLookup
	status   GeoDatabaseStatus
	dataDir  string
	ipv6     bool
	client   *http.Client
	now      func() time.Time
	rename   func(*os.Root, string, string) error
}

func NewGeoManager(dataDir string, ipv6 bool) *GeoManager {
	if dataDir == "" {
		dataDir = "data"
	}
	g := &GeoManager{dataDir: dataDir, ipv6: ipv6, now: time.Now, client: &http.Client{Timeout: 90 * time.Second, CheckRedirect: geoCheckRedirect, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 20 * time.Second}}}
	g.status = GeoDatabaseStatus{Source: g.source(), IPv4Only: !ipv6}
	g.load()
	return g
}
func (g *GeoManager) source() string {
	if g.ipv6 {
		return geoIPv6Source
	}
	return geoIPv4Source
}
func (g *GeoManager) names() (string, string) {
	if g.ipv6 {
		return "ipv6wry.db", "ipv6wry.meta.json"
	}
	return "qqwry.dat", "qqwry.meta.json"
}
func (g *GeoManager) parse(raw []byte) (geoLookup, error) {
	if g.ipv6 {
		return ParseIPv6GeoDB(raw)
	}
	return ParseQQWry(raw)
}
func (g *GeoManager) Status() GeoDatabaseStatus { g.mu.RLock(); defer g.mu.RUnlock(); return g.status }
func (g *GeoManager) Lookup(address netip.Addr) (QQWryLocation, bool) {
	g.mu.RLock()
	table := g.table
	g.mu.RUnlock()
	if table == nil {
		return QQWryLocation{}, false
	}
	return table.Lookup(address)
}

func (g *GeoManager) renamePath(root *os.Root, oldName, newName string) error {
	if g.rename != nil {
		return g.rename(root, oldName, newName)
	}
	return root.Rename(oldName, newName)
}

func (g *GeoManager) openRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(g.dataDir, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(g.dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if create {
		if err = root.Mkdir("geoip", 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return root.OpenRoot("geoip")
}
func geoRead(root *os.Root, name string, limit int64) ([]byte, os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, nil, errGeoUpdate
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, nil, errGeoUpdate
	}
	return raw, info, nil
}
func geoSHA(raw []byte) string { hash := sha256.Sum256(raw); return hex.EncodeToString(hash[:]) }
func (g *GeoManager) load() {
	root, err := g.openRoot(false)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			g.status.Error = "IP 数据库目录无法读取"
		}
		return
	}
	defer root.Close()
	name, metaName := g.names()
	raw, info, err := geoRead(root, name, geoMaxBytes)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			g.status.Error = "IP 数据库文件无法读取"
		}
		return
	}
	table, err := g.parse(raw)
	if err != nil {
		g.status.Error = "IP 数据库损坏，请重新安装或更新"
		return
	}
	status := GeoDatabaseStatus{Ready: true, Version: table.Version(), DataVersion: table.Version(), UpdatedAt: info.ModTime().UTC().Format(time.RFC3339), Records: table.Records(), Bytes: int64(len(raw)), Source: "manual", IPv4Only: !g.ipv6, SHA256: geoSHA(raw)}
	metaRaw, _, err := geoRead(root, metaName, 16<<10)
	var meta geoMetadata
	if err == nil && json.Unmarshal(metaRaw, &meta) == nil && meta.SHA256 == status.SHA256 && (meta.Bytes == 0 || meta.Bytes == status.Bytes) && (meta.Records == 0 || meta.Records == status.Records) && (meta.Source == g.source() || meta.Source == "manual") {
		if _, err := time.Parse(time.RFC3339, meta.UpdatedAt); err == nil {
			status.UpdatedAt = meta.UpdatedAt
		}
		if geoVersionPattern.MatchString(meta.Version) {
			status.Version = meta.Version
		}
		status.Source = meta.Source
	}
	g.table, g.status = table, status
}

func geoAllowedURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "raw.githubusercontent.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	}
	return false
}
func geoCheckRedirect(r *http.Request, via []*http.Request) error {
	if len(via) >= 5 || !geoAllowedURL(r.URL) {
		return errGeoUpdate
	}
	return nil
}
func (g *GeoManager) fetch(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !geoAllowedURL(u) {
		return nil, errGeoUpdate
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errGeoUpdate
	}
	r.Header.Set("User-Agent", "ganwen-blog-offline-geo/1")
	r.Header.Set("Accept", "application/vnd.github+json")
	response, err := g.client.Do(r)
	if err != nil {
		return nil, errGeoUpdate
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return nil, errGeoUpdate
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errGeoUpdate
	}
	return raw, nil
}

func (g *GeoManager) download(ctx context.Context) ([]byte, geoMetadata, error) {
	meta := geoMetadata{Source: g.source(), UpdatedAt: g.now().UTC().Format(time.RFC3339)}
	if g.ipv6 {
		raw, err := g.fetch(ctx, geoIPv6ContentsAPI, 1<<20)
		if err != nil {
			return nil, meta, err
		}
		var content struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &content) != nil || content.Type != "file" || content.Path != "src/libs/ipv6wry.db" || content.Size < 64 || content.Size > geoV6MaxBytes || len(content.SHA) != 40 {
			return nil, meta, errGeoUpdate
		}
		sha, err := hex.DecodeString(content.SHA)
		if err != nil || len(sha) != 20 || strings.ToLower(content.SHA) != content.SHA {
			return nil, meta, errGeoUpdate
		}
		raw, err = g.fetch(ctx, "https://api.github.com/repos/itbdw/ip-database/git/blobs/"+content.SHA, 12<<20)
		if err != nil {
			return nil, meta, err
		}
		var blob struct {
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
			SHA      string `json:"sha"`
			Size     int64  `json:"size"`
		}
		if json.Unmarshal(raw, &blob) != nil || blob.Encoding != "base64" || blob.SHA != content.SHA || blob.Size != content.Size {
			return nil, meta, errGeoUpdate
		}
		data, err := base64.StdEncoding.DecodeString(blob.Content)
		if err != nil || int64(len(data)) != content.Size {
			return nil, meta, errGeoUpdate
		}
		h := sha1.New()
		fmt.Fprintf(h, "blob %d\x00", len(data))
		h.Write(data)
		if hex.EncodeToString(h.Sum(nil)) != content.SHA {
			return nil, meta, errGeoUpdate
		}
		meta.Version = "git-" + content.SHA[:12]
		meta.GitBlobSHA1 = content.SHA
		meta.SHA256 = geoSHA(data)
		meta.Bytes = int64(len(data))
		return data, meta, nil
	}
	raw, err := g.fetch(ctx, geoIPv4ReleaseAPI, 1<<20)
	if err != nil {
		return nil, meta, err
	}
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if json.Unmarshal(raw, &release) != nil || len(release.Tag) != 8 {
		return nil, meta, errGeoUpdate
	}
	if _, err = time.Parse("20060102", release.Tag); err != nil {
		return nil, meta, errGeoUpdate
	}
	for _, asset := range release.Assets {
		if asset.Name != "qqwry.dat" {
			continue
		}
		expected := "https://github.com/metowolf/qqwry.dat/releases/download/" + release.Tag + "/qqwry.dat"
		if asset.URL != expected || asset.Size < 15 || asset.Size > geoMaxBytes || !strings.HasPrefix(asset.Digest, "sha256:") {
			return nil, meta, errGeoUpdate
		}
		digest := strings.TrimPrefix(asset.Digest, "sha256:")
		if !validAnalyticsHash(digest) {
			return nil, meta, errGeoUpdate
		}
		data, err := g.fetch(ctx, expected, geoMaxBytes)
		if err != nil || int64(len(data)) != asset.Size || geoSHA(data) != digest {
			return nil, meta, errGeoUpdate
		}
		meta.Version, meta.SHA256, meta.Bytes = release.Tag, digest, int64(len(data))
		return data, meta, nil
	}
	return nil, meta, errGeoUpdate
}

func geoWriteStage(root *os.Root, name string, raw []byte) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}
func geoSyncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	err = f.Sync()
	if runtime.GOOS == "windows" {
		return nil
	}
	return err
}

// Database and metadata are staged before any canonical file changes. Old
// hardlinks permit rollback if either rename fails; startup verifies the paired
// SHA so a crash between the two atomic renames cannot advertise a false version.
func (g *GeoManager) install(raw []byte, meta geoMetadata) error {
	root, err := g.openRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	nonce := make([]byte, 12)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	prefix := ".geo-" + hex.EncodeToString(nonce)
	name, metaName := g.names()
	names := []string{name, metaName}
	stages := []string{prefix + ".db", prefix + ".meta"}
	olds := []string{prefix + ".old-db", prefix + ".old-meta"}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	oldExists := []bool{false, false}
	changed := []bool{false, false}
	rollbackFailed := false
	defer func() {
		for _, stage := range stages {
			_ = root.Remove(stage)
		}
		if !rollbackFailed {
			for _, old := range olds {
				_ = root.Remove(old)
			}
		}
	}()
	for i, name := range names {
		info, e := root.Lstat(name)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errGeoUpdate
		}
		if err = root.Link(name, olds[i]); err != nil {
			return err
		}
		oldExists[i] = true
	}
	if err = geoWriteStage(root, stages[0], raw); err != nil {
		return err
	}
	if err = geoWriteStage(root, stages[1], metaRaw); err != nil {
		return err
	}
	rollback := func(cause error) error {
		for i := 1; i >= 0; i-- {
			if !changed[i] {
				continue
			}
			var e error
			if oldExists[i] {
				e = g.renamePath(root, olds[i], names[i])
			} else {
				e = root.Remove(names[i])
			}
			if e != nil {
				rollbackFailed = true
			}
		}
		_ = geoSyncRoot(root)
		return cause
	}
	// Metadata first: a interrupted update can always load the old valid DB.
	for _, i := range []int{1, 0} {
		if err = g.renamePath(root, stages[i], names[i]); err != nil {
			return rollback(err)
		}
		changed[i] = true
	}
	if err = geoSyncRoot(root); err != nil {
		return rollback(err)
	}
	return nil
}
func (g *GeoManager) Update(ctx context.Context) (GeoDatabaseStatus, error) {
	if !g.updating.TryLock() {
		return g.Status(), errGeoBusy
	}
	defer g.updating.Unlock()
	// The server write deadline is 90s. Reserve time for validation, atomic
	// publication and the JSON response after upstream networking completes.
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return g.Status(), errGeoUpdate
	}
	raw, meta, err := g.download(ctx)
	if err != nil {
		return g.Status(), errGeoUpdate
	}
	table, err := g.parse(raw)
	if err != nil {
		return g.Status(), errGeoUpdate
	}
	meta.Records = table.Records()
	if ctx.Err() != nil {
		return g.Status(), errGeoUpdate
	}
	if err = g.install(raw, meta); err != nil {
		return g.Status(), errGeoUpdate
	}
	status := GeoDatabaseStatus{Ready: true, Version: meta.Version, DataVersion: table.Version(), UpdatedAt: meta.UpdatedAt, Records: meta.Records, Bytes: meta.Bytes, Source: meta.Source, IPv4Only: !g.ipv6, SHA256: meta.SHA256}
	g.mu.Lock()
	g.table, g.status = table, status
	g.mu.Unlock()
	return status, nil
}

func (a *Analytics) handleGeoUpdate(w http.ResponseWriter, r *http.Request, ipv6 bool) {
	if r.ContentLength > 4096 {
		adminError(w, 400, "请求内容过大")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body map[string]json.RawMessage
	if !readAdminJSON(w, r, &body) {
		return
	}
	if body == nil || len(body) != 0 {
		adminError(w, 400, "更新请求只需空对象")
		return
	}
	manager, key := a.geo4, "geo_database"
	if ipv6 {
		manager, key = a.geo6, "geo_database_v6"
	}
	status, err := manager.Update(r.Context())
	if errors.Is(err, errGeoBusy) {
		adminError(w, 409, "IP 数据库正在更新，请稍后再试")
		return
	}
	if err != nil {
		adminError(w, 502, "更新未完成，请稍后重试；现有 IP 数据库继续使用")
		return
	}
	adminJSON(w, 200, map[string]GeoDatabaseStatus{key: status})
}
