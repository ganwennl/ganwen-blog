package blog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrIndependentPageInvalid = errors.New("独立页面设置不正确")
var ErrIndependentPagePathConflict = errors.New("页面地址已被其他页面使用")

// Keep custom page addresses distinct from query-based archives, language
// prefixes and media paths. No percent encoding is accepted, so a stored path
// has exactly one spelling before and after HTTP URL decoding.
var independentPagePathPattern = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9/_-]*(?:\.html)?/?$`)

func validPublicPagePath(value string) error {
	if value == "" {
		return nil
	}
	invalid := fmt.Errorf("%w：地址须以 / 开头，仅使用英文字母、数字、连字符、下划线，可以 .html 结尾；不能占用系统或媒体地址", ErrIndependentPageInvalid)
	if len(value) > 512 || !independentPagePathPattern.MatchString(value) || strings.Contains(value, "//") {
		return invalid
	}
	first := strings.ToLower(strings.Split(strings.TrimPrefix(value, "/"), "/")[0])
	for _, language := range publicLanguages() {
		if first == language {
			return invalid
		}
	}
	switch first {
	case "admin", "wp-admin", "wp-content", "wp-includes", "wp-json", "assets", "analytics", "data", "deploy", "migration", "dist", "ganwen-go", "license", "readme", "readme.html", ".well-known", "robots", "sitemap", "index":
		return invalid
	}
	// Exact reserved file routes mostly fail the extension rule already. Keep
	// explicit checks so widening the accepted suffixes cannot take them over.
	switch strings.ToLower(value) {
	case "/index.php", "/robots.txt", "/sitemap.xml", "/wp-sitemap.xml", "/favicon.ico", "/wp-login.php", "/xmlrpc.php":
		return invalid
	}
	return nil
}

func publicIndependentPagePath(p Post, lang string) string {
	if p.Type != "page" || p.PublicPath == "" || validPublicPagePath(p.PublicPath) != nil {
		return ""
	}
	return publicLanguagePrefix(lang) + p.PublicPath
}

// Ordinary WordPress canonicals stay untouched. Changing a managed page's
// address also updates its canonical, including a return to the ID-based URL.
func (s *Store) updateIndependentPageCanonical(ctx context.Context, p *Post, old Post) error {
	if old.PublicPath == p.PublicPath {
		return nil
	}
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	base := strings.TrimRight(settings.BaseURL, "/")
	p.CanonicalURL = base + p.PublicPath
	if p.PublicPath == "" {
		p.CanonicalURL = fmt.Sprintf("%s/?page_id=%d", base, p.ID)
	}
	return nil
}

// GetPublicPageByPath includes unlisted pages without putting them in any
// discoverable list. The shared snapshot retains the SQL publication boundary.
func (s *Store) GetPublicPageByPath(ctx context.Context, requestPath string) (Post, error) {
	if requestPath == "" || validPublicPagePath(requestPath) != nil {
		return Post{}, ErrNotFound
	}
	snapshot, err := s.postSnapshot(ctx)
	if err != nil {
		return Post{}, err
	}
	var match *Post
	for i, p := range snapshot.posts {
		if p.Type != "page" || p.PublicPath != requestPath {
			continue
		}
		// Legacy/manual duplicate documents must not pick an arbitrary page.
		if match != nil {
			return Post{}, ErrNotFound
		}
		copy := p
		match = &copy
		if !snapshot.published[i] {
			return Post{}, ErrNotFound
		}
	}
	if match == nil {
		return Post{}, ErrNotFound
	}
	return clonePost(*match, false), nil
}

func validateIndependentPagePaths(posts []Post) error {
	paths := make(map[string]bool)
	for _, p := range posts {
		if err := validateIndependentPage(p); err != nil {
			return err
		}
		if p.PublicPath == "" {
			continue
		}
		if paths[p.PublicPath] {
			return ErrIndependentPagePathConflict
		}
		paths[p.PublicPath] = true
	}
	return nil
}

func validateIndependentPage(p Post) error {
	if p.Type != "page" && (p.PublicPath != "" || p.Unlisted) {
		return fmt.Errorf("%w：独立地址及仅地址访问只能用于页面", ErrIndependentPageInvalid)
	}
	if p.Unlisted && p.PublicPath == "" {
		return fmt.Errorf("%w：仅地址访问的页面必须填写独立地址", ErrIndependentPageInvalid)
	}
	return validPublicPagePath(p.PublicPath)
}

// Called inside the same write transaction as the document update; SavePost,
// UpdatePost and NewPost also hold writeMu. Draft/private paths stay reserved.
func checkIndependentPagePathAvailable(ctx context.Context, tx *sql.Tx, candidate Post) error {
	if err := validateIndependentPage(candidate); err != nil {
		return err
	}
	if candidate.PublicPath == "" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,document FROM posts WHERE kind='page'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw string
		var p Post
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if id == candidate.ID {
			continue
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		if p.PublicPath == candidate.PublicPath {
			return ErrIndependentPagePathConflict
		}
	}
	return rows.Err()
}
