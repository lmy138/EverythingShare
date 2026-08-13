package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

const (
	maxManifestEntries = 250000
	maxZipEntries      = 100000
	smallZipEntries    = 10000
)

//go:embed web/*
var webFiles embed.FS

type config struct {
	ListenAddr        string
	PublicBaseURL     string
	EverythingBaseURL string
	EverythingAuth    string
	SessionSecret     []byte
	AdminKey          string
	DatabasePath      string
	CacheDir          string
	CookieSecure      bool
	TrustProxyHeaders bool
	ZipThreshold      int64
	ZipCacheMax       int64
	ZipTTL            time.Duration
	ZipMinFree        uint64
	Standalone        bool
	BasicAuthUsername string
	BasicAuthHash     []byte
	OpenBrowser       bool
}

type app struct {
	cfg        config
	db         *sql.DB
	httpClient *http.Client
	zipLocks   sync.Map
}

type everythingItem struct {
	Type         string      `json:"type"`
	Name         string      `json:"name"`
	Path         string      `json:"path"`
	SizeValue    interface{} `json:"size"`
	DateModified interface{} `json:"date_modified"`
}

type everythingResponse struct {
	TotalResults int              `json:"totalResults"`
	Results      []everythingItem `json:"results"`
}

type shareRecord struct {
	ID           string
	Token        string
	Title        string
	SourcePath   string
	SourceType   string
	SourceName   string
	SourceSize   int64
	SourceMod    string
	CodeHash     string
	ExpiresAt    sql.NullInt64
	MaxDownloads sql.NullInt64
	Downloads    int64
	Status       string
	CreatedBy    string
	CreatedAt    int64
	UpdatedAt    int64
	EntryCount   int64
	TotalSize    int64
}

type entryRecord struct {
	ID           string
	ShareID      string
	RelativePath string
	ParentPath   string
	Name         string
	Kind         string
	Size         int64
	Modified     string
}

type createShareRequest struct {
	SourcePath   string `json:"sourcePath"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	ExpiresAt    string `json:"expiresAt"`
	MaxDownloads *int64 `json:"maxDownloads"`
	Code         string `json:"code"`
}

type downloadRequest struct {
	EntryID string `json:"entryId"`
	Zip     bool   `json:"zip"`
}

type resetCodeRequest struct {
	Code string `json:"code"`
}

type verifyRequest struct {
	Code string `json:"code"`
}

func main() {
	if handled, err := handleStandaloneCommand(os.Args[1:]); handled {
		if err != nil {
			exitStandaloneError(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		addr := env("LISTEN_ADDR", "0.0.0.0:8080")
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			os.Exit(2)
		}
		resp, err := http.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(2)
		}
		_ = resp.Body.Close()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if len(os.Args) != 3 {
			log.Fatal("usage: share-gateway backup /absolute/output.db")
		}
		if err := backupDatabase(env("DATABASE_PATH", "/data/share-gateway.db"), os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		exitStandaloneError(err)
	}
	if err := os.MkdirAll(path.Dir(strings.ReplaceAll(cfg.DatabasePath, "\\", "/")), 0700); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	a := &app{
		cfg: cfg,
		db:  db,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy:                 nil,
				MaxIdleConns:          20,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		},
	}
	if err := a.migrate(); err != nil {
		log.Fatal(err)
	}
	go a.cacheJanitor()

	handler := a.routes()
	if cfg.Standalone {
		handler, err = a.standaloneRoutes()
		if err != nil {
			log.Fatal(err)
		}
	}
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(shutdownSignals)
	go func() {
		<-shutdownSignals
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("everything share gateway listening on %s", cfg.ListenAddr)
	if cfg.Standalone {
		log.Printf("open %s in your browser", cfg.PublicBaseURL)
		if cfg.OpenBrowser {
			go openBrowserAfterStart(cfg.PublicBaseURL)
		}
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("server stopped: %v", err)
	}
}

func backupDatabase(source, target string) error {
	if !path.IsAbs(target) || target == "/" || strings.ContainsRune(target, '\x00') {
		return errors.New("backup target must be a specific absolute path")
	}
	if path.Clean(source) == path.Clean(target) {
		return errors.New("backup target must differ from the live database")
	}
	if err := os.MkdirAll(path.Dir(target), 0700); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	quoted := strings.ReplaceAll(target, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + quoted + "'"); err != nil {
		return fmt.Errorf("sqlite backup: %w", err)
	}
	return nil
}

func loadConfig() (config, error) {
	if !hasGatewayEnvironment() {
		return loadStandaloneConfig(standaloneConfigPath(os.Args[1:]))
	}
	session, err := decodeSecret(os.Getenv("SESSION_SECRET"))
	if err != nil || len(session) < 32 {
		return config{}, errors.New("SESSION_SECRET must contain at least 32 random bytes")
	}
	adminKey := os.Getenv("ADMIN_SHARED_KEY")
	if len(adminKey) < 32 {
		return config{}, errors.New("ADMIN_SHARED_KEY must be at least 32 characters")
	}
	publicBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/")
	if err := validateBaseURL("PUBLIC_BASE_URL", publicBaseURL); err != nil {
		return config{}, err
	}
	everythingBaseURL := strings.TrimRight(env("EVERYTHING_BASE_URL", "http://host.docker.internal:8081"), "/")
	if err := validateBaseURL("EVERYTHING_BASE_URL", everythingBaseURL); err != nil {
		return config{}, err
	}
	auth := strings.TrimSpace(os.Getenv("EVERYTHING_AUTH_HEADER"))
	if auth == "" {
		username := os.Getenv("EVERYTHING_USERNAME")
		password := os.Getenv("EVERYTHING_PASSWORD")
		if username != "" || password != "" {
			auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		}
	}
	if !strings.HasPrefix(auth, "Basic ") {
		return config{}, errors.New("set EVERYTHING_AUTH_HEADER or EVERYTHING_USERNAME and EVERYTHING_PASSWORD")
	}
	return config{
		ListenAddr:        env("LISTEN_ADDR", "0.0.0.0:8080"),
		PublicBaseURL:     publicBaseURL,
		EverythingBaseURL: everythingBaseURL,
		EverythingAuth:    auth,
		SessionSecret:     session,
		AdminKey:          adminKey,
		DatabasePath:      env("DATABASE_PATH", "/data/share-gateway.db"),
		CacheDir:          env("CACHE_DIR", "/cache"),
		CookieSecure:      envBool("COOKIE_SECURE", strings.HasPrefix(publicBaseURL, "https://")),
		TrustProxyHeaders: envBool("TRUST_PROXY_HEADERS", false),
		ZipThreshold:      envInt64("ZIP_CACHE_THRESHOLD_BYTES", 5<<30),
		ZipCacheMax:       envInt64("ZIP_CACHE_MAX_BYTES", 50<<30),
		ZipTTL:            time.Duration(envInt64("ZIP_CACHE_TTL_HOURS", 24)) * time.Hour,
		ZipMinFree:        uint64(envInt64("ZIP_CACHE_MIN_FREE_BYTES", 20<<30)),
	}, nil
}

func validateBaseURL(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("%s must be an absolute http or https URL", name)
	}
	return nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt64(name string, fallback int64) int64 {
	v, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func envBool(name string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return parsed
}

func decodeSecret(v string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(v); err == nil {
		return b, nil
	}
	return base64.StdEncoding.DecodeString(v)
}

func (a *app) migrate() error {
	_, err := a.db.Exec(`
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS shares (
  id TEXT PRIMARY KEY,
  token TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  source_path TEXT NOT NULL,
  source_type TEXT NOT NULL CHECK(source_type IN ('file','folder')),
  source_name TEXT NOT NULL,
  source_size INTEGER NOT NULL DEFAULT 0,
  source_modified TEXT NOT NULL DEFAULT '',
  code_hash TEXT NOT NULL,
  expires_at INTEGER,
  max_downloads INTEGER,
  download_count INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'active',
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  entry_count INTEGER NOT NULL DEFAULT 0,
  total_size INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS entries (
  id TEXT PRIMARY KEY,
  share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  relative_path TEXT NOT NULL,
  parent_path TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK(kind IN ('file','folder')),
  size INTEGER NOT NULL DEFAULT 0,
  modified TEXT NOT NULL DEFAULT '',
  UNIQUE(share_id, relative_path)
);
CREATE INDEX IF NOT EXISTS entries_parent_idx ON entries(share_id, parent_path, kind, name);
CREATE TABLE IF NOT EXISTS tickets (
  id TEXT PRIMARY KEY,
  share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  entry_id TEXT,
  mode TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tickets_expiry_idx ON tickets(expires_at);
CREATE TABLE IF NOT EXISTS failed_attempts (
  share_id TEXT NOT NULL,
  ip_hash TEXT NOT NULL,
  failed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS attempts_idx ON failed_attempts(share_id, ip_hash, failed_at);
CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  share_id TEXT,
  event TEXT NOT NULL,
  actor TEXT NOT NULL,
  occurred_at INTEGER NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
`)
	if err != nil {
		return err
	}
	// Existing installations only stored a one-way hash. Keep that hash for
	// verification and add an authenticated encrypted copy so the protected
	// management UI can reproduce a one-click URL without storing codes in
	// plaintext. SQLite lacks ADD COLUMN IF NOT EXISTS on older versions.
	if _, err := a.db.Exec(`ALTER TABLE shares ADD COLUMN code_cipher TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
		return err
	}
	// Revoked shares no longer belong in the management queue. Foreign-key
	// cascades remove their frozen manifests and outstanding tickets, while
	// audit_events intentionally remain as the operational history.
	if _, err := a.db.Exec(`DELETE FROM shares WHERE status='revoked'`); err != nil {
		return err
	}
	if _, err := a.db.Exec(`DELETE FROM failed_attempts WHERE share_id NOT IN (SELECT id FROM shares)`); err != nil {
		return err
	}
	return nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /assets/{name}", a.asset)
	mux.HandleFunc("GET /s/{token}", a.sharePage)
	mux.HandleFunc("POST /api/v1/public/shares/{token}/verify", a.verifyCode)
	mux.HandleFunc("GET /api/v1/public/shares/{token}/entries", a.publicEntries)
	mux.HandleFunc("POST /api/v1/public/shares/{token}/downloads", a.createDownload)
	mux.HandleFunc("GET /d/{ticket}", a.download)
	mux.HandleFunc("GET /share-admin/", a.withAdmin(a.adminPage))
	mux.HandleFunc("GET /share-admin/assets/{name}", a.withAdmin(a.asset))
	mux.HandleFunc("GET /share-api/v1/session", a.withAdmin(a.adminSession))
	mux.HandleFunc("POST /share-api/v1/shares", a.withAdmin(a.createShare))
	mux.HandleFunc("GET /share-api/v1/shares", a.withAdmin(a.listShares))
	mux.HandleFunc("POST /share-api/v1/shares/{id}/revoke", a.withAdmin(a.revokeShare))
	mux.HandleFunc("POST /share-api/v1/shares/{id}/refresh", a.withAdmin(a.refreshShare))
	mux.HandleFunc("POST /share-api/v1/shares/{id}/reset-code", a.withAdmin(a.resetCode))
	return a.securityHeaders(a.requestLog(mux))
}

func (a *app) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if r.URL.Path != "/healthz" {
			log.Printf("%s %s %s", r.Method, safeLogPath(r.URL.Path), time.Since(start).Round(time.Millisecond))
		}
	})
}

func safeLogPath(p string) string {
	switch {
	case strings.HasPrefix(p, "/s/"):
		return "/s/[redacted]"
	case strings.HasPrefix(p, "/d/"):
		return "/d/[redacted]"
	case strings.Contains(p, "/public/shares/"):
		return strings.Replace(p, strings.Split(p, "/")[5], "[redacted]", 1)
	default:
		return p
	}
}

func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.db.PingContext(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.EverythingBaseURL+"/?json=1&count=0", nil)
	req.Header.Set("Authorization", a.cfg.EverythingAuth)
	resp, err := a.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	_ = resp.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *app) asset(w http.ResponseWriter, r *http.Request) {
	name := path.Base(r.PathValue("name"))
	b, err := fs.ReadFile(webFiles, "web/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch path.Ext(name) {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	default:
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

func (a *app) sharePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !validToken(token) {
		http.NotFound(w, r)
		return
	}
	html, _ := fs.ReadFile(webFiles, "web/share.html")
	html = bytes.ReplaceAll(html, []byte("{{TOKEN}}"), []byte(token))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(html)
}

func (a *app) adminPage(w http.ResponseWriter, r *http.Request) {
	html, _ := fs.ReadFile(webFiles, "web/admin.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(html)
}

func (a *app) withAdmin(next func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-Share-Admin-Key")
		user := strings.TrimSpace(r.Header.Get("X-Auth-Request-User"))
		if subtle.ConstantTimeCompare([]byte(provided), []byte(a.cfg.AdminKey)) != 1 || user == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin authentication required"})
			return
		}
		next(w, r)
	}
}

func (a *app) adminSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (a *app) createShare(w http.ResponseWriter, r *http.Request) {
	var in createShareRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	source, err := cleanWindowsPath(in.SourcePath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "文件路径无效")
		return
	}
	item, err := a.findExact(r.Context(), source)
	if err != nil {
		writeError(w, http.StatusNotFound, "Everything 中未找到该文件或文件夹")
		return
	}
	kind := normalizeType(item.Type)
	if kind == "" {
		writeError(w, http.StatusBadRequest, "不支持该对象类型")
		return
	}
	if in.Type != "" && in.Type != kind {
		writeError(w, http.StatusConflict, "文件类型已变化，请刷新页面")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		code = randomCode()
	}
	if !regexp.MustCompile(`^[A-Z0-9]{4,12}$`).MatchString(code) {
		writeError(w, http.StatusBadRequest, "提取码必须为4至12位字母或数字")
		return
	}
	expires, err := parseExpiry(in.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "有效期无效")
		return
	}
	if in.MaxDownloads != nil && *in.MaxDownloads <= 0 {
		writeError(w, http.StatusBadRequest, "下载次数必须大于0")
		return
	}
	codeHash, err := hashCode(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法生成提取码")
		return
	}
	codeCipher, err := a.encryptShareCode(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保护提取码")
		return
	}
	id := randomID(16)
	token := randomID(24)
	now := time.Now().Unix()
	name := item.Name
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = name
	}
	if len([]rune(title)) > 120 {
		writeError(w, http.StatusBadRequest, "标题过长")
		return
	}
	size := itemSize(item)
	modified := itemModified(item)

	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "数据库暂不可用")
		return
	}
	defer tx.Rollback()
	var expiresValue any
	if expires > 0 {
		expiresValue = expires
	}
	var maxValue any
	if in.MaxDownloads != nil {
		maxValue = *in.MaxDownloads
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO shares
		(id,token,title,source_path,source_type,source_name,source_size,source_modified,code_hash,code_cipher,expires_at,max_downloads,status,created_by,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, token, title, source, kind, name, size, modified, codeHash, codeCipher, expiresValue, maxValue, "active",
		r.Header.Get("X-Auth-Request-User"), now, now)
	if err != nil {
		writeError(w, 500, "无法创建分享")
		return
	}
	var entryCount, totalSize int64
	if kind == "folder" {
		entries, err := a.snapshotFolder(r.Context(), source)
		if err != nil {
			writeError(w, http.StatusBadGateway, "无法读取文件夹内容")
			return
		}
		if len(entries) > maxManifestEntries {
			writeError(w, http.StatusRequestEntityTooLarge, "文件夹项目过多，超过当前安全上限")
			return
		}
		stmt, err := tx.PrepareContext(r.Context(), `INSERT INTO entries(id,share_id,relative_path,parent_path,name,kind,size,modified) VALUES(?,?,?,?,?,?,?,?)`)
		if err != nil {
			writeError(w, 500, "无法保存文件夹清单")
			return
		}
		for _, e := range entries {
			if _, err = stmt.ExecContext(r.Context(), e.ID, id, e.RelativePath, e.ParentPath, e.Name, e.Kind, e.Size, e.Modified); err != nil {
				_ = stmt.Close()
				writeError(w, 500, "无法保存文件夹清单")
				return
			}
			entryCount++
			if e.Kind == "file" {
				totalSize += e.Size
			}
		}
		_ = stmt.Close()
		if _, err = tx.ExecContext(r.Context(), `UPDATE shares SET entry_count=?,total_size=? WHERE id=?`, entryCount, totalSize, id); err != nil {
			writeError(w, 500, "无法保存分享统计")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "无法提交分享")
		return
	}
	a.audit(id, "created", r.Header.Get("X-Auth-Request-User"), "")
	baseURL := a.cfg.PublicBaseURL + "/s/" + token
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "url": directShareURL(baseURL, code), "baseUrl": baseURL, "code": code,
		"expiresAt": unixOrNil(expires), "maxDownloads": in.MaxDownloads,
		"entryCount": entryCount, "totalSize": totalSize,
	})
}

func (a *app) listShares(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,token,title,source_path,source_type,source_name,code_cipher,expires_at,max_downloads,download_count,status,created_at,updated_at,entry_count,total_size
		FROM shares WHERE status<>'revoked' ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		writeError(w, 500, "无法读取分享列表")
		return
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var id, token, title, sourcePath, typ, name, codeCipher, status string
		var expires, max sql.NullInt64
		var downloads, created, updated, count, total int64
		if err := rows.Scan(&id, &token, &title, &sourcePath, &typ, &name, &codeCipher, &expires, &max, &downloads, &status, &created, &updated, &count, &total); err != nil {
			continue
		}
		code, _ := a.decryptShareCode(codeCipher)
		baseURL := a.cfg.PublicBaseURL + "/s/" + token
		result = append(result, map[string]any{
			"id": id, "url": directShareURL(baseURL, code), "baseUrl": baseURL, "hasDirectCode": code != "",
			"title": title, "type": typ, "name": name, "sourcePath": sourcePath,
			"expiresAt": nullableUnix(expires), "maxDownloads": nullableInt(max), "downloads": downloads,
			"status": effectiveStatus(status, expires), "createdAt": created, "updatedAt": updated,
			"entryCount": count, "totalSize": total,
		})
	}
	writeJSON(w, 200, map[string]any{"shares": result})
}

func (a *app) revokeShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, err := a.db.ExecContext(r.Context(), `DELETE FROM shares WHERE id=?`, id)
	if err != nil {
		writeError(w, 500, "撤销失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "分享不存在")
		return
	}
	_, _ = a.db.ExecContext(r.Context(), `DELETE FROM failed_attempts WHERE share_id=?`, id)
	_ = os.Remove(a.cachePath(id))
	a.audit(id, "revoked_and_deleted", r.Header.Get("X-Auth-Request-User"), "")
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (a *app) resetCode(w http.ResponseWriter, r *http.Request) {
	var in resetCodeRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, 400, "请求格式不正确")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		code = randomCode()
	}
	if !regexp.MustCompile(`^[A-Z0-9]{4,12}$`).MatchString(code) {
		writeError(w, 400, "提取码必须为4至12位字母或数字")
		return
	}
	hash, err := hashCode(code)
	if err != nil {
		writeError(w, 500, "无法生成提取码")
		return
	}
	codeCipher, err := a.encryptShareCode(code)
	if err != nil {
		writeError(w, 500, "无法保护提取码")
		return
	}
	id := r.PathValue("id")
	res, err := a.db.ExecContext(r.Context(), `UPDATE shares SET code_hash=?,code_cipher=?,updated_at=? WHERE id=?`, hash, codeCipher, time.Now().Unix(), id)
	if err != nil {
		writeError(w, 500, "重置失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, 404, "分享不存在")
		return
	}
	_, _ = a.db.ExecContext(r.Context(), `DELETE FROM tickets WHERE share_id=?`, id)
	var token string
	if err := a.db.QueryRowContext(r.Context(), `SELECT token FROM shares WHERE id=?`, id).Scan(&token); err != nil {
		writeError(w, 500, "无法读取更新后的分享")
		return
	}
	a.audit(id, "code_reset", r.Header.Get("X-Auth-Request-User"), "")
	baseURL := a.cfg.PublicBaseURL + "/s/" + token
	writeJSON(w, 200, map[string]string{"code": code, "url": directShareURL(baseURL, code), "baseUrl": baseURL})
}

func (a *app) refreshShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, err := a.loadShareByID(r.Context(), id)
	if err != nil {
		writeError(w, 404, "分享不存在")
		return
	}
	item, err := a.findExact(r.Context(), s.SourcePath)
	if err != nil {
		writeError(w, 409, "源文件或文件夹已不存在")
		return
	}
	entries := []entryRecord{}
	if s.SourceType == "folder" {
		entries, err = a.snapshotFolder(r.Context(), s.SourcePath)
		if err != nil || len(entries) > maxManifestEntries {
			writeError(w, 409, "无法刷新文件夹清单")
			return
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "数据库暂不可用")
		return
	}
	defer tx.Rollback()
	_, _ = tx.ExecContext(r.Context(), `DELETE FROM entries WHERE share_id=?`, id)
	var total int64
	for _, e := range entries {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO entries(id,share_id,relative_path,parent_path,name,kind,size,modified) VALUES(?,?,?,?,?,?,?,?)`,
			e.ID, id, e.RelativePath, e.ParentPath, e.Name, e.Kind, e.Size, e.Modified); err != nil {
			writeError(w, 500, "无法保存清单")
			return
		}
		if e.Kind == "file" {
			total += e.Size
		}
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE shares SET source_size=?,source_modified=?,entry_count=?,total_size=?,updated_at=? WHERE id=?`,
		itemSize(item), itemModified(item), len(entries), total, time.Now().Unix(), id)
	if err != nil || tx.Commit() != nil {
		writeError(w, 500, "刷新失败")
		return
	}
	_, _ = a.db.ExecContext(r.Context(), `DELETE FROM tickets WHERE share_id=?`, id)
	_ = os.Remove(a.cachePath(id))
	a.audit(id, "refreshed", r.Header.Get("X-Auth-Request-User"), "")
	writeJSON(w, 200, map[string]any{"refreshed": true, "entryCount": len(entries), "totalSize": total})
}

func (a *app) verifyCode(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	s, err := a.loadActiveShareByToken(r.Context(), token)
	if err != nil {
		writeError(w, 404, "分享不存在或已失效")
		return
	}
	ipHash := a.ipHash(a.clientIP(r))
	cutoff := time.Now().Add(-15 * time.Minute).Unix()
	var failures int
	_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM failed_attempts WHERE share_id=? AND ip_hash=? AND failed_at>=?`, s.ID, ipHash, cutoff).Scan(&failures)
	if failures >= 5 {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}
	var in verifyRequest
	if decodeJSON(r, &in) != nil || !verifyCodeHash(strings.ToUpper(strings.TrimSpace(in.Code)), s.CodeHash) {
		_, _ = a.db.ExecContext(r.Context(), `INSERT INTO failed_attempts(share_id,ip_hash,failed_at) VALUES(?,?,?)`, s.ID, ipHash, time.Now().Unix())
		writeError(w, http.StatusUnauthorized, "提取码错误")
		return
	}
	_, _ = a.db.ExecContext(r.Context(), `DELETE FROM failed_attempts WHERE share_id=? AND ip_hash=?`, s.ID, ipHash)
	exp := time.Now().Add(12 * time.Hour).Unix()
	if s.ExpiresAt.Valid && exp > s.ExpiresAt.Int64 {
		exp = s.ExpiresAt.Int64
	}
	cookieName := a.guestCookieName(token)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: a.signSession(s.ID, exp), Path: "/", Expires: time.Unix(exp, 0),
		Secure: a.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	a.audit(s.ID, "verified", "guest:"+ipHash[:12], "")
	writeJSON(w, 200, map[string]any{
		"verified": true, "title": s.Title, "type": s.SourceType, "name": s.SourceName,
		"expiresAt": nullableUnix(s.ExpiresAt), "downloads": s.Downloads, "maxDownloads": nullableInt(s.MaxDownloads),
		"entryCount": s.EntryCount, "totalSize": s.TotalSize,
	})
}

func (a *app) publicEntries(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	s, ok := a.authorizedGuest(r, token)
	if !ok {
		writeError(w, 401, "请先输入提取码")
		return
	}
	if s.SourceType != "folder" {
		writeJSON(w, 200, map[string]any{"entries": []any{}, "path": ""})
		return
	}
	parent := normalizeRelativePath(r.URL.Query().Get("path"))
	rows, err := a.db.QueryContext(r.Context(), `SELECT id,relative_path,name,kind,size,modified FROM entries WHERE share_id=? AND parent_path=? ORDER BY kind DESC,name COLLATE NOCASE LIMIT 5000`, s.ID, parent)
	if err != nil {
		writeError(w, 500, "无法读取文件夹")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, rel, name, kind, modified string
		var size int64
		if rows.Scan(&id, &rel, &name, &kind, &size, &modified) == nil {
			items = append(items, map[string]any{"id": id, "path": rel, "name": name, "type": kind, "size": size, "modified": modified})
		}
	}
	writeJSON(w, 200, map[string]any{
		"title": s.Title, "path": parent, "entries": items,
		"canZip":  s.EntryCount <= maxZipEntries,
		"zipMode": zipMode(s, a.cfg.ZipThreshold),
	})
}

func (a *app) createDownload(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	s, ok := a.authorizedGuest(r, token)
	if !ok {
		writeError(w, 401, "请先输入提取码")
		return
	}
	var in downloadRequest
	if decodeJSON(r, &in) != nil {
		writeError(w, 400, "请求格式不正确")
		return
	}
	mode := "file"
	entryID := ""
	if s.SourceType == "folder" {
		if in.Zip {
			if s.EntryCount > maxZipEntries {
				writeError(w, 409, "该文件夹项目过多，不支持整包下载")
				return
			}
			mode = "zip"
		} else {
			e, err := a.loadEntry(r.Context(), s.ID, in.EntryID)
			if err != nil || e.Kind != "file" {
				writeError(w, 404, "文件不存在")
				return
			}
			entryID = e.ID
		}
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, 500, "数据库暂不可用")
		return
	}
	defer tx.Rollback()
	var max sql.NullInt64
	var count int64
	var status string
	var expires sql.NullInt64
	if err := tx.QueryRowContext(r.Context(), `SELECT max_downloads,download_count,status,expires_at FROM shares WHERE id=?`, s.ID).Scan(&max, &count, &status, &expires); err != nil {
		writeError(w, 404, "分享不存在")
		return
	}
	if status != "active" || (expires.Valid && expires.Int64 <= time.Now().Unix()) || (max.Valid && count >= max.Int64) {
		writeError(w, 410, "分享已失效或下载次数已用完")
		return
	}
	id := randomID(24)
	ticketExp := time.Now().Add(24 * time.Hour).Unix()
	if expires.Valid && ticketExp > expires.Int64 {
		ticketExp = expires.Int64
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO tickets(id,share_id,entry_id,mode,expires_at,created_at) VALUES(?,?,?,?,?,?)`, id, s.ID, nullableString(entryID), mode, ticketExp, time.Now().Unix()); err != nil {
		writeError(w, 500, "无法创建下载")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE shares SET download_count=download_count+1,updated_at=? WHERE id=?`, time.Now().Unix(), s.ID); err != nil {
		writeError(w, 500, "无法更新下载次数")
		return
	}
	if tx.Commit() != nil {
		writeError(w, 500, "无法创建下载")
		return
	}
	a.audit(s.ID, "download_ticket", "guest", mode)
	writeJSON(w, 201, map[string]any{"url": a.cfg.PublicBaseURL + "/d/" + id, "expiresAt": ticketExp, "mode": mode})
}

func (a *app) download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("ticket")
	if !validToken(id) {
		http.NotFound(w, r)
		return
	}
	var shareID, mode string
	var entryID sql.NullString
	var ticketExp int64
	err := a.db.QueryRowContext(r.Context(), `SELECT share_id,entry_id,mode,expires_at FROM tickets WHERE id=?`, id).Scan(&shareID, &entryID, &mode, &ticketExp)
	if err != nil || ticketExp <= time.Now().Unix() {
		writeError(w, 410, "下载链接已失效")
		return
	}
	s, err := a.loadShareByID(r.Context(), shareID)
	if err != nil || !shareTicketUsable(s) {
		writeError(w, 410, "分享已失效")
		return
	}
	if mode == "zip" {
		a.downloadZip(w, r, s)
		return
	}
	source := s.SourcePath
	name := s.SourceName
	size := s.SourceSize
	modified := s.SourceMod
	if entryID.Valid {
		e, err := a.loadEntry(r.Context(), s.ID, entryID.String)
		if err != nil {
			writeError(w, 404, "文件不存在")
			return
		}
		source = joinWindowsPath(s.SourcePath, e.RelativePath)
		name, size, modified = e.Name, e.Size, e.Modified
	}
	current, err := a.findExact(r.Context(), source)
	if err != nil || itemSize(current) != size || itemModified(current) != modified {
		writeError(w, 409, "源文件已变化，请联系分享者刷新分享")
		return
	}
	resp, err := a.openEverything(r.Context(), source, r.Header.Get("Range"))
	if err != nil {
		writeError(w, 502, "源文件暂不可用")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		writeError(w, 502, "源文件暂不可用")
		return
	}
	copyHeader(w.Header(), resp.Header, "Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *app) downloadZip(w http.ResponseWriter, r *http.Request, s shareRecord) {
	cacheEligible := s.TotalSize <= a.cfg.ZipThreshold && s.EntryCount <= smallZipEntries && a.cacheHasSpace()
	if cacheEligible {
		lockValue, _ := a.zipLocks.LoadOrStore(s.ID, &sync.Mutex{})
		lock := lockValue.(*sync.Mutex)
		lock.Lock()
		defer lock.Unlock()
		cacheFile := a.cachePath(s.ID)
		if _, err := os.Stat(cacheFile); errors.Is(err, os.ErrNotExist) {
			if err := a.buildCachedZip(r.Context(), s, cacheFile); err != nil {
				log.Printf("zip build failed for share %s: %v", s.ID, err)
				writeError(w, 409, "文件夹内容已变化，请联系分享者刷新分享")
				return
			}
		}
		f, err := os.Open(cacheFile)
		if err != nil {
			writeError(w, 500, "缓存文件暂不可用")
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		_ = os.Chtimes(cacheFile, time.Now(), time.Now())
		w.Header().Set("Content-Disposition", contentDisposition(s.SourceName+".zip"))
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Cache-Control", "private, no-store")
		http.ServeContent(w, r, s.SourceName+".zip", info.ModTime(), f)
		return
	}
	w.Header().Set("Content-Disposition", contentDisposition(s.SourceName+".zip"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Zip-Mode", "stream")
	zw := zip.NewWriter(w)
	defer zw.Close()
	rows, err := a.db.QueryContext(r.Context(), `SELECT relative_path,size FROM entries WHERE share_id=? AND kind='file' ORDER BY relative_path`, s.ID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		var size int64
		if rows.Scan(&rel, &size) != nil {
			continue
		}
		resp, err := a.openEverything(r.Context(), joinWindowsPath(s.SourcePath, rel), "")
		if err != nil || resp.StatusCode != 200 || (resp.ContentLength >= 0 && resp.ContentLength != size) {
			if resp != nil {
				_ = resp.Body.Close()
			}
			return
		}
		h := &zip.FileHeader{Name: strings.ReplaceAll(rel, "\\", "/"), Method: zip.Deflate}
		h.SetModTime(time.Now())
		part, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(part, resp.Body)
		}
		_ = resp.Body.Close()
		if err != nil {
			return
		}
	}
}

func (a *app) buildCachedZip(ctx context.Context, s shareRecord, target string) error {
	tmp := target + ".tmp-" + randomID(6)
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	zw := zip.NewWriter(f)
	rows, err := a.db.QueryContext(ctx, `SELECT relative_path,size FROM entries WHERE share_id=? AND kind='file' ORDER BY relative_path`, s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		var size int64
		if err := rows.Scan(&rel, &size); err != nil {
			return err
		}
		resp, err := a.openEverything(ctx, joinWindowsPath(s.SourcePath, rel), "")
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 || (resp.ContentLength >= 0 && resp.ContentLength != size) {
			_ = resp.Body.Close()
			return errors.New("source changed")
		}
		h := &zip.FileHeader{Name: strings.ReplaceAll(rel, "\\", "/"), Method: zip.Deflate}
		h.SetModTime(time.Now())
		part, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(part, resp.Body)
		}
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	ok = true
	return nil
}

func (a *app) cachePath(shareID string) string {
	return path.Join(a.cfg.CacheDir, shareID+".zip")
}

func (a *app) cacheHasSpace() bool {
	available, err := availableDiskBytes(a.cfg.CacheDir)
	if err != nil {
		return false
	}
	return available >= a.cfg.ZipMinFree
}

func (a *app) cacheJanitor() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		a.cleanCache()
		<-ticker.C
	}
}

func (a *app) cleanCache() {
	files, err := os.ReadDir(a.cfg.CacheDir)
	if err != nil {
		return
	}
	type cached struct {
		path string
		size int64
		mod  time.Time
	}
	var all []cached
	var total int64
	now := time.Now()
	for _, e := range files {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := path.Join(a.cfg.CacheDir, e.Name())
		if now.Sub(info.ModTime()) > a.cfg.ZipTTL {
			_ = os.Remove(p)
			continue
		}
		all = append(all, cached{p, info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.Before(all[j].mod) })
	for _, f := range all {
		if total <= a.cfg.ZipCacheMax {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	_, _ = a.db.Exec(`DELETE FROM tickets WHERE expires_at<?`, now.Unix())
	_, _ = a.db.Exec(`DELETE FROM failed_attempts WHERE failed_at<?`, now.Add(-24*time.Hour).Unix())
}

func (a *app) findExact(ctx context.Context, source string) (everythingItem, error) {
	query := `"` + strings.ReplaceAll(source, `"`, `""`) + `"`
	items, err := a.searchEverything(ctx, query, 0, 200)
	if err != nil {
		return everythingItem{}, err
	}
	for _, item := range items {
		if strings.EqualFold(itemFullPath(item), source) {
			return item, nil
		}
	}
	return everythingItem{}, sql.ErrNoRows
}

func (a *app) snapshotFolder(ctx context.Context, root string) ([]entryRecord, error) {
	var out []entryRecord
	offset := 0
	// Everything treats spaces and punctuation as query syntax. Restrict the
	// search to the selected directory explicitly so Unicode and spaced paths
	// produce a complete immutable manifest.
	query := `path:"` + strings.ReplaceAll(root, `"`, `""`) + `"`
	for {
		items, err := a.searchEverything(ctx, query, offset, 1000)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			full := itemFullPath(item)
			if !strings.HasPrefix(strings.ToLower(full), strings.ToLower(root+"\\")) {
				continue
			}
			rel := strings.TrimPrefix(full[len(root):], "\\")
			rel = normalizeRelativePath(rel)
			if rel == "" || strings.Contains(rel, "..\\") {
				continue
			}
			out = append(out, entryRecord{
				ID: randomID(16), RelativePath: rel, ParentPath: relativeParent(rel), Name: item.Name,
				Kind: normalizeType(item.Type), Size: itemSize(item), Modified: itemModified(item),
			})
			if len(out) > maxManifestEntries {
				return out, nil
			}
		}
		if len(items) < 1000 {
			break
		}
		offset += len(items)
	}
	return out, nil
}

func (a *app) searchEverything(ctx context.Context, search string, offset, count int) ([]everythingItem, error) {
	u, _ := url.Parse(a.cfg.EverythingBaseURL + "/")
	q := u.Query()
	q.Set("search", search)
	q.Set("json", "1")
	q.Set("path_column", "1")
	q.Set("size_column", "1")
	q.Set("date_modified_column", "1")
	q.Set("offset", strconv.Itoa(offset))
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Authorization", a.cfg.EverythingAuth)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("everything search returned %d", resp.StatusCode)
	}
	var result everythingResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&result); err != nil {
		return nil, err
	}
	return result.Results, nil
}

func (a *app) openEverything(ctx context.Context, source, byteRange string) (*http.Response, error) {
	u := a.cfg.EverythingBaseURL + everythingDownloadPath(source)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", a.cfg.EverythingAuth)
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	return a.httpClient.Do(req)
}

func everythingDownloadPath(source string) string {
	parts := strings.Split(strings.ReplaceAll(source, "/", "\\"), "\\")
	for i, part := range parts {
		escaped := url.PathEscape(part)
		escaped = strings.ReplaceAll(escaped, ":", "%3A")
		parts[i] = escaped
	}
	return "/" + strings.Join(parts, "/")
}

func (a *app) loadShareByID(ctx context.Context, id string) (shareRecord, error) {
	return scanShare(a.db.QueryRowContext(ctx, `SELECT id,token,title,source_path,source_type,source_name,source_size,source_modified,code_hash,expires_at,max_downloads,download_count,status,created_by,created_at,updated_at,entry_count,total_size FROM shares WHERE id=?`, id))
}

func (a *app) loadActiveShareByToken(ctx context.Context, token string) (shareRecord, error) {
	if !validToken(token) {
		return shareRecord{}, sql.ErrNoRows
	}
	s, err := scanShare(a.db.QueryRowContext(ctx, `SELECT id,token,title,source_path,source_type,source_name,source_size,source_modified,code_hash,expires_at,max_downloads,download_count,status,created_by,created_at,updated_at,entry_count,total_size FROM shares WHERE token=?`, token))
	if err != nil || !shareUsable(s) {
		return shareRecord{}, sql.ErrNoRows
	}
	return s, nil
}

type rowScanner interface{ Scan(...any) error }

func scanShare(row rowScanner) (shareRecord, error) {
	var s shareRecord
	err := row.Scan(&s.ID, &s.Token, &s.Title, &s.SourcePath, &s.SourceType, &s.SourceName, &s.SourceSize, &s.SourceMod,
		&s.CodeHash, &s.ExpiresAt, &s.MaxDownloads, &s.Downloads, &s.Status, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &s.EntryCount, &s.TotalSize)
	return s, err
}

func (a *app) loadEntry(ctx context.Context, shareID, id string) (entryRecord, error) {
	var e entryRecord
	err := a.db.QueryRowContext(ctx, `SELECT id,share_id,relative_path,parent_path,name,kind,size,modified FROM entries WHERE share_id=? AND id=?`, shareID, id).
		Scan(&e.ID, &e.ShareID, &e.RelativePath, &e.ParentPath, &e.Name, &e.Kind, &e.Size, &e.Modified)
	return e, err
}

func shareUsable(s shareRecord) bool {
	now := time.Now().Unix()
	return s.Status == "active" && (!s.ExpiresAt.Valid || s.ExpiresAt.Int64 > now) && (!s.MaxDownloads.Valid || s.Downloads < s.MaxDownloads.Int64)
}

// A ticket is issued only after the download limit is atomically consumed.
// Keep that ticket usable for retries and Range requests, while still making
// revocation and expiry effective immediately.
func shareTicketUsable(s shareRecord) bool {
	now := time.Now().Unix()
	return s.Status == "active" && (!s.ExpiresAt.Valid || s.ExpiresAt.Int64 > now)
}

func (a *app) authorizedGuest(r *http.Request, token string) (shareRecord, bool) {
	if !validToken(token) {
		return shareRecord{}, false
	}
	s, err := scanShare(a.db.QueryRowContext(r.Context(), `SELECT id,token,title,source_path,source_type,source_name,source_size,source_modified,code_hash,expires_at,max_downloads,download_count,status,created_by,created_at,updated_at,entry_count,total_size FROM shares WHERE token=?`, token))
	if err != nil || !shareTicketUsable(s) {
		return shareRecord{}, false
	}
	cookie, err := r.Cookie(a.guestCookieName(token))
	if err != nil {
		return shareRecord{}, false
	}
	shareID, exp, ok := a.verifySession(cookie.Value)
	return s, ok && shareID == s.ID && exp > time.Now().Unix()
}

func (a *app) guestCookieName(token string) string {
	sum := sha256.Sum256([]byte(token))
	prefix := "sg_"
	if a.cfg.CookieSecure {
		prefix = "__Host-sg_"
	}
	return prefix + hex.EncodeToString(sum[:6])
}

func (a *app) signSession(shareID string, exp int64) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(shareID + "|" + strconv.FormatInt(exp, 10)))
	mac := hmac.New(sha256.New, a.cfg.SessionSecret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *app) verifySession(value string) (string, int64, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return "", 0, false
	}
	mac := hmac.New(sha256.New, a.cfg.SessionSecret)
	_, _ = mac.Write([]byte(parts[0]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	fields := strings.Split(string(raw), "|")
	if err != nil || len(fields) != 2 {
		return "", 0, false
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	return fields[0], exp, err == nil
}

func hashCode(code string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(code), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=2$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func verifyCodeHash(code, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	expected, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil {
		return false
	}
	actual := argon2.IDKey([]byte(code), salt, 3, 64*1024, 2, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func (a *app) encryptShareCode(code string) (string, error) {
	key := sha256.Sum256(a.cfg.SessionSecret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(code), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (a *app) decryptShareCode(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256(a.cfg.SessionSecret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("encrypted share code is truncated")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func directShareURL(baseURL, code string) string {
	if code == "" {
		return baseURL
	}
	// A fragment provides Baidu-style one-click extraction without sending the
	// extraction code to Nginx access logs, HTTP referrers, or the Go server.
	return baseURL + "#pwd=" + url.QueryEscape(code)
}

func parseExpiry(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Now().Add(7 * 24 * time.Hour).Unix(), nil
	}
	if v == "permanent" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil || t.Before(time.Now().Add(time.Minute)) {
		return 0, errors.New("invalid expiry")
	}
	return t.Unix(), nil
}

func cleanWindowsPath(v string) (string, error) {
	v = strings.TrimSpace(strings.ReplaceAll(v, "/", "\\"))
	if !regexp.MustCompile(`^[A-Za-z]:\\`).MatchString(v) {
		return "", errors.New("absolute Windows path required")
	}
	parts := strings.Split(v, "\\")
	out := []string{strings.ToUpper(parts[0])}
	for _, p := range parts[1:] {
		if p == "" || p == "." {
			continue
		}
		if p == ".." || strings.ContainsAny(p, "\x00") {
			return "", errors.New("unsafe path")
		}
		out = append(out, p)
	}
	if len(out) == 1 {
		return out[0] + "\\", nil
	}
	return strings.Join(out, "\\"), nil
}

func normalizeRelativePath(v string) string {
	v = strings.Trim(strings.ReplaceAll(v, "/", "\\"), "\\")
	parts := strings.Split(v, "\\")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" && p != "." && p != ".." {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\\")
}

func relativeParent(v string) string {
	v = normalizeRelativePath(v)
	if i := strings.LastIndex(v, "\\"); i >= 0 {
		return v[:i]
	}
	return ""
}

func joinWindowsPath(root, relative string) string {
	return strings.TrimRight(root, "\\") + "\\" + normalizeRelativePath(relative)
}

func itemFullPath(item everythingItem) string {
	if item.Path == "" {
		return item.Name
	}
	return strings.TrimRight(item.Path, "\\/") + "\\" + item.Name
}

func normalizeType(v string) string {
	v = strings.ToLower(v)
	if v == "folder" || v == "directory" {
		return "folder"
	}
	if v == "file" {
		return "file"
	}
	return ""
}

func itemSize(item everythingItem) int64 {
	return valueInt64(item.SizeValue)
}

func itemModified(item everythingItem) string {
	switch v := item.DateModified.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func valueInt64(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.ReplaceAll(n, ",", ""), 10, 64)
		return i
	default:
		return 0
	}
}

func randomID(bytesLen int) string {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomCode() string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	b := make([]byte, 4)
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	for i := range b {
		b[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(b)
}

func validToken(v string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9_-]{20,64}$`).MatchString(v)
}

func contentDisposition(name string) string {
	name = strings.ReplaceAll(strings.ReplaceAll(name, "\r", ""), "\n", "")
	fallback := regexp.MustCompile(`[^\x20-\x7e]`).ReplaceAllString(name, "_")
	fallback = strings.ReplaceAll(fallback, `"`, "'")
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, url.PathEscape(name))
}

func copyHeader(dst, src http.Header, names ...string) {
	for _, name := range names {
		if v := src.Get(name); v != "" {
			dst.Set(name, v)
		}
	}
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func nullableUnix(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func unixOrNil(v int64) any {
	if v > 0 {
		return v
	}
	return nil
}

func nullableInt(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func effectiveStatus(status string, expires sql.NullInt64) string {
	if status == "active" && expires.Valid && expires.Int64 <= time.Now().Unix() {
		return "expired"
	}
	return status
}

func zipMode(s shareRecord, threshold int64) string {
	if s.EntryCount > maxZipEntries {
		return "disabled"
	}
	if s.EntryCount <= smallZipEntries && s.TotalSize <= threshold {
		return "cached"
	}
	return "stream"
}

func (a *app) clientIP(r *http.Request) string {
	if a.cfg.TrustProxyHeaders {
		if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); v != "" {
			return v
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host != "" {
		return host
	}
	return r.RemoteAddr
}

func (a *app) ipHash(ip string) string {
	mac := hmac.New(sha256.New, a.cfg.SessionSecret)
	_, _ = mac.Write([]byte(time.Now().UTC().Format("2006-01-02") + "|" + ip))
	return hex.EncodeToString(mac.Sum(nil))
}

func (a *app) audit(shareID, event, actor, detail string) {
	if len(detail) > 256 {
		detail = detail[:256]
	}
	_, _ = a.db.Exec(`INSERT INTO audit_events(share_id,event,actor,occurred_at,detail) VALUES(?,?,?,?,?)`,
		nullableString(shareID), event, actor, time.Now().Unix(), detail)
}
