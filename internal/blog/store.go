package blog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("article changed; reload before saving")

type Store struct {
	db      *sql.DB
	driver  string
	writeMu sync.Mutex
	postsMu sync.Mutex
	posts   *postSnapshot
}

func OpenStore(path string) (*Store, error) {
	return OpenDatabase(DatabaseConfig{Driver: "sqlite", Path: path})
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) GetValue(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE `key`=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}
func (s *Store) SetValue(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, s.kvUpsertSQL(), key, value)
	return err
}
func (s *Store) Settings(ctx context.Context) (SiteSettings, error) {
	v, err := s.GetValue(ctx, "site")
	if errors.Is(err, ErrNotFound) {
		return SiteSettings{Title: "甘文", BaseURL: "https://www.ganwen.nl", PostsPerPage: 10}, nil
	}
	var out SiteSettings
	if err == nil {
		err = json.Unmarshal([]byte(v), &out)
	}
	return out, err
}
func (s *Store) SaveSettings(ctx context.Context, p SiteSettings) error {
	if err := validateContactNoticeSettings(p); err != nil {
		return err
	}
	if err := validatePublicDailyViews(p.PublicDailyViews); err != nil {
		return err
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("site URL must be an HTTPS origin")
	}
	if p.PostsPerPage < 1 || p.PostsPerPage > 100 {
		p.PostsPerPage = 10
	}
	if _, err := normalizedSidebarAds(p.SidebarAds); err != nil {
		return err
	}
	v, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.SetValue(ctx, "site", string(v))
}
func (s *Store) GetPost(ctx context.Context, id int64) (Post, error) {
	var v string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM posts WHERE id=?", id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return Post{}, ErrNotFound
	}
	var p Post
	if err == nil {
		err = json.Unmarshal([]byte(v), &p)
	}
	return p, err
}
func (s *Store) ListPosts(ctx context.Context, includeNonPublic bool) ([]Post, error) {
	snapshot, err := s.postSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	count := snapshot.publicCount
	if includeNonPublic {
		count = len(snapshot.posts)
	}
	out := make([]Post, 0, count)
	for i, p := range snapshot.posts {
		if includeNonPublic || snapshot.public[i] {
			out = append(out, clonePost(p, false))
		}
	}
	return out, nil
}
func (s *Store) saveLocked(ctx context.Context, p *Post) error {
	v, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkIndependentPagePathAvailable(ctx, tx, *p); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.postUpsertSQL(), p.ID, p.Type, p.Status, p.PublishedAt, string(v)); err != nil {
		return err
	}
	if err = s.markPostsChanged(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func validPost(p Post) error {
	if p.ID <= 0 {
		return errors.New("invalid ID")
	}
	switch p.Type {
	case "post", "page", "attachment":
	default:
		return errors.New("invalid type")
	}
	switch p.Status {
	case "publish", "draft", "private", "pending", "future", "trash":
	default:
		return errors.New("invalid status")
	}
	if err := validateIndependentPage(p); err != nil {
		return err
	}
	if len(p.Title) > 4096 || len(p.ContentHTML) > 4<<20 {
		return errors.New("article too large")
	}
	if p.Translations == nil {
		return nil
	}
	for lang, t := range p.Translations {
		if !isTranslationLanguage(lang) {
			return errors.New("invalid translation language")
		}
		if t.Status != "review" && t.Status != "publish" && t.Status != "draft" {
			return errors.New("invalid translation status")
		}
		if t.Status == "publish" && t.SourceHash != SourceHash(p) {
			return errors.New("translation is outdated")
		}
	}
	return nil
}
func invalidateTranslations(p *Post) {
	for lang, t := range p.Translations {
		if t.SourceHash != SourceHash(*p) && t.Status == "publish" {
			t.Status = "review"
			p.Translations[lang] = t
		}
	}
}
func (s *Store) SavePost(ctx context.Context, p *Post) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	old, err := s.GetPost(ctx, p.ID)
	if err != nil {
		return err
	}
	if old.Version != p.Version {
		return ErrConflict
	}
	p.CanonicalURL = old.CanonicalURL
	p.Type = old.Type
	p.SourceHTML = old.SourceHTML
	if err = s.updateIndependentPageCanonical(ctx, p, old); err != nil {
		return err
	}
	if SourceHash(old) != SourceHash(*p) {
		invalidateTranslations(p)
	}
	if err = validPost(*p); err != nil {
		return err
	}
	p.Version++
	return s.saveLocked(ctx, p)
}
func (s *Store) UpdatePost(ctx context.Context, id int64, fn func(*Post) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	p, err := s.GetPost(ctx, id)
	if err != nil {
		return err
	}
	old := p
	oldHash := SourceHash(p)
	if err = fn(&p); err != nil {
		return err
	}
	p.ID = old.ID
	p.Type = old.Type
	p.CanonicalURL = old.CanonicalURL
	p.SourceHTML = old.SourceHTML
	if err = s.updateIndependentPageCanonical(ctx, &p, old); err != nil {
		return err
	}
	if oldHash != SourceHash(p) {
		invalidateTranslations(&p)
	}
	if err = validPost(p); err != nil {
		return err
	}
	p.Version++
	return s.saveLocked(ctx, &p)
}
func (s *Store) NewPost(ctx context.Context, p *Post) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var id int64
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM posts").Scan(&id); err != nil {
		return err
	}
	if id < 9999 {
		id = 9999
	}
	if id == int64(^uint64(0)>>1) {
		return errors.New("article IDs exhausted")
	}
	id++
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	p.ID = id
	p.Version = 1
	if p.Type == "" {
		p.Type = "post"
	}
	if p.Status == "" {
		p.Status = "draft"
	}
	key := "p"
	if p.Type == "page" {
		key = "page_id"
	}
	p.CanonicalURL = fmt.Sprintf("%s/?%s=%d", settings.BaseURL, key, id)
	if p.PublicPath != "" {
		p.CanonicalURL = strings.TrimRight(settings.BaseURL, "/") + p.PublicPath
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if p.PublishedAt == "" {
		p.PublishedAt = now
	}
	p.ModifiedAt = now
	if p.Author == "" {
		p.Author = "甘"
		p.AuthorID = 1
	}
	if p.Translations == nil {
		p.Translations = map[string]Translation{}
	}
	if err = validPost(*p); err != nil {
		return err
	}
	return s.saveLocked(ctx, p)
}
