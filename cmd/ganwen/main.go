package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"ganwen-blog/internal/blog"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("Ganwen Blog " + version)
		return nil
	}
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	data := fs.String("data", "data", "private data directory")
	media := fs.String("media", "", "media root (default data/media)")
	listen := fs.String("listen", "127.0.0.1:8097", "listen address")
	preview := fs.Bool("preview", true, "prevent indexing this preview")
	worker := fs.Bool("worker", true, "run the automatic translation worker")
	siteURL := fs.String("site-url", "", "HTTPS site origin (saved in site settings)")
	defaultFile := "migration/wordpress.json"
	if command == "database-export" || command == "database-import" {
		defaultFile = "migration/blog-database.json"
	}
	if command == "import-independent-page" {
		defaultFile = "migration/independent-page.json"
	}
	file := fs.String("file", defaultFile, "import/export document path")
	database := fs.String("database", "", "sqlite or mysql (default GBLOG_DATABASE_DRIVER, then sqlite)")
	keyFile := fs.String("key-file", "", "separate original translation key for database-import")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dataDir, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	if command == "init-admin" {
		return initAdmin(dataDir)
	}
	switch command {
	case "serve", "import", "import-independent-page", "queue", "database-export", "database-import":
	default:
		return errors.New("unknown command")
	}
	if err = loadEnv(filepath.Join(dataDir, "runtime.env")); err != nil {
		return err
	}
	driver := *database
	if driver == "" {
		driver = os.Getenv("GBLOG_DATABASE_DRIVER")
	}
	dbPath := os.Getenv("GBLOG_DATABASE_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "blog.db")
	}
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(dataDir, dbPath)
	}
	dsn := ""
	if strings.EqualFold(driver, "mysql") {
		dsn = os.Getenv("GBLOG_DATABASE_DSN")
	}
	store, err := blog.OpenDatabase(blog.DatabaseConfig{Driver: driver, DSN: dsn, Path: dbPath})
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "database-export" {
		if err = store.ExportDatabaseFile(ctx, *file, dataDir); err == nil {
			fmt.Println("Database export completed; encrypted configuration and jobs retained. Preserve the separate .translation.key backup privately.")
		}
		return err
	}
	if command == "database-import" {
		if err = store.ImportDatabaseFile(ctx, *file, *keyFile, dataDir); err == nil {
			fmt.Println("Database import completed into empty target; original IDs, URLs, states and encrypted configuration retained.")
		}
		return err
	}
	if command == "import" {
		n, err := store.ImportFile(ctx, *file)
		if err == nil {
			fmt.Printf("Imported %d records; originals and publication states retained.\n", n)
		}
		return err
	}
	mediaDir := *media
	if mediaDir == "" {
		mediaDir = filepath.Join(dataDir, "media")
	}
	mediaDir, err = filepath.Abs(mediaDir)
	if err != nil {
		return err
	}
	if command == "import-independent-page" {
		id, created, err := store.ImportIndependentPageFile(ctx, *file, mediaDir)
		if err == nil {
			fmt.Printf("Independent page verified: id=%d created=%t; original HTML and evidence retained.\n", id, created)
		}
		return err
	}
	if err = os.MkdirAll(mediaDir, 0700); err != nil {
		return err
	}
	settings, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	if command == "serve" {
		settings, err = configureSite(ctx, store, *siteURL)
		if err != nil {
			return err
		}
	}
	cfg := blog.Config{DataDir: dataDir, MediaDir: mediaDir, BaseURL: settings.BaseURL, ListenAddr: *listen, Preview: *preview, AdminUser: os.Getenv("GBLOG_ADMIN_USER"), AdminPasswordHash: os.Getenv("GBLOG_ADMIN_PASSWORD_HASH"), TranslationEndpoint: os.Getenv("GBLOG_TRANSLATION_ENDPOINT"), TranslationKey: os.Getenv("GBLOG_TRANSLATION_KEY"), TranslationModel: os.Getenv("GBLOG_TRANSLATION_MODEL"), AutoPublishTranslation: os.Getenv("GBLOG_TRANSLATION_AUTOPUBLISH") == "true"}
	app, err := blog.NewApp(store, cfg)
	if err != nil {
		return err
	}
	if command == "queue" {
		posts, err := store.ListPosts(ctx, false)
		if err != nil {
			return err
		}
		n := 0
		for _, p := range posts {
			if p.Type != "attachment" {
				if err = app.Translator.Queue(ctx, p, false); err != nil {
					return err
				}
				n++
			}
		}
		fmt.Printf("Queued translations for %d public documents.\n", n)
		return nil
	}
	if command != "serve" {
		return fmt.Errorf("unknown command: %s", command)
	}
	if cfg.Preview {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil {
			return err
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return errors.New("preview must listen on loopback")
		}
	}
	server := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 45 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	if *worker {
		go app.RunWorker(ctx)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("Ganwen listening on %s (preview=%t)", *listen, *preview)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

var version = "1.0.2"

func configureSite(ctx context.Context, store *blog.Store, siteURL string) (blog.SiteSettings, error) {
	settings, err := store.Settings(ctx)
	if err != nil {
		return settings, err
	}
	_, err = store.GetValue(ctx, "site")
	if errors.Is(err, blog.ErrNotFound) {
		virtualViews, showStats := false, true
		settings = blog.SiteSettings{Title: "Ganwen Blog", BaseURL: "https://localhost", PostsPerPage: 10,
			PublicVirtualViewsEnabled: &virtualViews, PublicStatsEnabled: &showStats}
	} else if err != nil {
		return settings, err
	}
	if siteURL != "" {
		settings.BaseURL = siteURL
	}
	if errors.Is(err, blog.ErrNotFound) || siteURL != "" {
		if saveErr := store.SaveSettings(ctx, settings); saveErr != nil {
			return settings, saveErr
		}
	}
	return settings, nil
}

func loadEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(key, "GBLOG_") {
			return errors.New("invalid runtime environment file")
		}
		if _, set := os.LookupEnv(key); !set {
			if err = os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
func initAdmin(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	envPath := filepath.Join(dir, "runtime.env")
	accessPath := filepath.Join(dir, "admin-access.txt")
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		return errors.New("runtime.env already exists; refusing to replace credentials")
	}
	if _, err := os.Stat(accessPath); !os.IsNotExist(err) {
		return errors.New("admin access file already exists")
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	password := base64.RawURLEncoding.EncodeToString(random)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	access := "后台账号：ganwen\n后台密码：" + password + "\n此文件仅用于本地领取管理员密码，请私密保存；无需粘贴到聊天。\n"
	f, err := os.OpenFile(accessPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(access)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	env := "GBLOG_ADMIN_USER=ganwen\nGBLOG_ADMIN_PASSWORD_HASH=" + string(hash) + "\n"
	f, err = os.OpenFile(envPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(env)
	closeErr = f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Printf("管理员凭据已生成：%s\n", accessPath)
	return nil
}
