package blog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type Term struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	URL      string `json:"url"`
	Taxonomy string `json:"taxonomy"`
}
type SEO struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}
type Translation struct {
	Title       string `json:"title"`
	ContentHTML string `json:"content_html"`
	ExcerptHTML string `json:"excerpt_html"`
	SourceHash  string `json:"source_hash"`
	Status      string `json:"status"`
	UpdatedAt   string `json:"updated_at"`
	MachineHash string `json:"machine_hash,omitempty"`
	SEO         SEO    `json:"seo"`
}
type Post struct {
	ID            int64                  `json:"id"`
	Version       int64                  `json:"version"`
	Type          string                 `json:"type"`
	Status        string                 `json:"status"`
	Slug          string                 `json:"slug"`
	Title         string                 `json:"title"`
	ContentHTML   string                 `json:"content_html"`
	SourceHTML    string                 `json:"source_html"`
	ExcerptHTML   string                 `json:"excerpt_html"`
	CanonicalURL  string                 `json:"canonical_url"`
	PublicPath    string                 `json:"public_path,omitempty"`
	Unlisted      bool                   `json:"unlisted,omitempty"`
	PublishedAt   string                 `json:"published_at"`
	ModifiedAt    string                 `json:"modified_at"`
	Author        string                 `json:"author"`
	AuthorID      int64                  `json:"author_id"`
	Categories    []Term                 `json:"categories"`
	Tags          []Term                 `json:"tags"`
	Format        string                 `json:"format"`
	FeaturedImage string                 `json:"featured_image"`
	Sticky        bool                   `json:"sticky"`
	SEO           SEO                    `json:"seo"`
	Translations  map[string]Translation `json:"translations"`
}

// wpdb exposes numeric IDs as strings in some PHP versions.
type legacyID int64

func (v *legacyID) UnmarshalJSON(raw []byte) error {
	n, err := strconv.ParseInt(strings.Trim(string(raw), "\""), 10, 64)
	if err == nil {
		*v = legacyID(n)
	}
	return err
}
func (p *Post) UnmarshalJSON(raw []byte) error {
	type alias Post
	var v struct {
		*alias
		ID legacyID `json:"id"`
	}
	v.alias = (*alias)(p)
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	p.ID = int64(v.ID)
	return nil
}

func SourceHash(p Post) string {
	h := sha256.Sum256([]byte(p.Title + "\x00" + p.ContentHTML + "\x00" + p.ExcerptHTML))
	return hex.EncodeToString(h[:])
}

type ContactLink struct {
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	URL        string `json:"url"`
	KindManual bool   `json:"kind_manual,omitempty"`
}

type SidebarAd struct {
	Title       string `json:"title"`
	ImageURL    string `json:"image_url"`
	TargetURL   string `json:"target_url"`
	Description string `json:"description,omitempty"`
	ButtonLabel string `json:"button_label,omitempty"`
	Sponsor     string `json:"sponsor,omitempty"`
}

type SidebarAdsConfig struct {
	Enabled         bool        `json:"enabled"`
	IntervalSeconds int         `json:"interval_seconds"` // Retained for older settings clients.
	Items           []SidebarAd `json:"items"`
}

type SiteSettings struct {
	Title                     string                       `json:"title"`
	Description               string                       `json:"description"`
	BaseURL                   string                       `json:"base_url"`
	PostsPerPage              int                          `json:"posts_per_page"`
	DefaultLayout             string                       `json:"default_layout"`
	ArticleSidebarEnabled     bool                         `json:"article_sidebar_enabled"`
	SidebarModules            *[]string                    `json:"sidebar_modules,omitempty"`
	SidebarCustomOrderEnabled bool                         `json:"sidebar_custom_order_enabled,omitempty"`
	SidebarOrder              []string                     `json:"sidebar_order,omitempty"`
	Categories                []Term                       `json:"categories"`
	Tags                      []Term                       `json:"tags"`
	HeaderImage               string                       `json:"header_image"`
	AvatarURL                 string                       `json:"avatar_url"`
	ContactLinks              []ContactLink                `json:"contact_links"`
	ContactNoticeEnabled      bool                         `json:"contact_notice_enabled,omitempty"`
	ContactNoticeText         string                       `json:"contact_notice_text,omitempty"`
	SidebarAds                SidebarAdsConfig             `json:"sidebar_ads,omitzero"`
	PublicDailyViews          *int64                       `json:"public_daily_views,omitempty"`
	PublicVirtualViewsEnabled *bool                        `json:"public_virtual_views_enabled,omitempty"`
	PublicStatsEnabled        *bool                        `json:"public_stats_enabled,omitempty"`
	FooterText                *string                      `json:"footer_text"`
	FooterTranslations        map[string]FooterTranslation `json:"footer_translations,omitempty"`
}
type Config struct {
	DataDir                string
	MediaDir               string
	BaseURL                string
	ListenAddr             string
	Preview                bool
	AdminUser              string
	AdminPasswordHash      string
	TranslationEndpoint    string
	TranslationKey         string
	TranslationModel       string
	AutoPublishTranslation bool
}
type App struct {
	Store      *Store
	Config     Config
	Admin      *Admin
	Translator *Translator
	Analytics  *Analytics
	mux        *http.ServeMux
}

func NewApp(store *Store, cfg Config) (*App, error) {
	a := &App{Store: store, Config: cfg, mux: http.NewServeMux()}
	var err error
	a.Translator, err = NewTranslator(store, cfg)
	if err != nil {
		return nil, err
	}
	a.Admin = NewAdmin(a)
	a.Admin.Register(a.mux)
	a.Analytics, err = NewAnalytics(a)
	if err != nil {
		return nil, err
	}
	a.mux.HandleFunc("/analytics/event", a.Analytics.handleEvent)
	a.mux.HandleFunc("/", a.handlePublic)
	return a, nil
}
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if a.Config.Preview {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		a.mux.ServeHTTP(w, r)
	})
}
func (a *App) RunWorker(ctx context.Context) { a.Translator.Run(ctx) }
