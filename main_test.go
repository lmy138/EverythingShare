package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestCleanWindowsPath(t *testing.T) {
	got, err := cleanWindowsPath(`c:/资料//项目/文件.txt`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\资料\项目\文件.txt` {
		t.Fatalf("unexpected path: %q", got)
	}
	for _, bad := range []string{`..\secret.txt`, `C:\safe\..\secret.txt`, `/etc/passwd`, `\\server\share`} {
		if _, err := cleanWindowsPath(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestCodeHash(t *testing.T) {
	encoded, err := hashCode("A2B4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "argon2id$") {
		t.Fatalf("unexpected encoding: %s", encoded)
	}
	if !verifyCodeHash("A2B4", encoded) {
		t.Fatal("correct code did not verify")
	}
	if verifyCodeHash("WRONG", encoded) {
		t.Fatal("wrong code verified")
	}
}

func TestSignedSession(t *testing.T) {
	a := &app{cfg: config{SessionSecret: []byte("0123456789abcdef0123456789abcdef")}}
	value := a.signSession("share-id", time.Now().Add(time.Hour).Unix())
	id, exp, ok := a.verifySession(value)
	if !ok || id != "share-id" || exp <= time.Now().Unix() {
		t.Fatalf("session failed: id=%q exp=%d ok=%v", id, exp, ok)
	}
	if _, _, ok := a.verifySession(value + "x"); ok {
		t.Fatal("tampered session verified")
	}
}

func TestEncryptedShareCode(t *testing.T) {
	a := &app{cfg: config{SessionSecret: []byte("0123456789abcdef0123456789abcdef")}}
	encrypted, err := a.encryptShareCode("A2B4")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "" || strings.Contains(encrypted, "A2B4") {
		t.Fatalf("share code was not protected: %q", encrypted)
	}
	decrypted, err := a.decryptShareCode(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != "A2B4" {
		t.Fatalf("unexpected decrypted code: %q", decrypted)
	}
	if _, err := a.decryptShareCode(encrypted + "x"); err == nil {
		t.Fatal("tampered encrypted code was accepted")
	}
}

func TestDirectShareURLUsesFragment(t *testing.T) {
	got := directShareURL("https://share.example/s/token", "A2B4")
	if got != "https://share.example/s/token#pwd=A2B4" {
		t.Fatalf("unexpected direct share URL: %q", got)
	}
	if got := directShareURL("https://share.example/s/token", ""); got != "https://share.example/s/token" {
		t.Fatalf("empty code changed the base URL: %q", got)
	}
}

func TestGuestCookieNameMatchesTransportSecurity(t *testing.T) {
	secure := &app{cfg: config{CookieSecure: true}}
	if got := secure.guestCookieName("abcdefghijklmnopqrst"); !strings.HasPrefix(got, "__Host-sg_") {
		t.Fatalf("secure cookie name = %q", got)
	}
	local := &app{cfg: config{CookieSecure: false}}
	if got := local.guestCookieName("abcdefghijklmnopqrst"); !strings.HasPrefix(got, "sg_") || strings.HasPrefix(got, "__Host-") {
		t.Fatalf("local cookie name = %q", got)
	}
}

func TestValidateBaseURL(t *testing.T) {
	for _, good := range []string{"https://share.example.com", "http://share.localhost:8088"} {
		if err := validateBaseURL("TEST_URL", good); err != nil {
			t.Fatalf("%s rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"", "share.example.com", "file:///tmp/share", "https://user@example.com"} {
		if err := validateBaseURL("TEST_URL", bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestRevokedSharesAreDeletedFromManagement(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &app{db: db, cfg: config{CacheDir: t.TempDir()}}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO shares(id,token,title,source_path,source_type,source_name,code_hash,status,created_by,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`
	now := time.Now().Unix()
	if _, err := db.Exec(insert, "old", "old-token", "old", `C:\old.txt`, "file", "old.txt", "hash", "revoked", "tester", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "active", "active-token", "active", `C:\active.txt`, "file", "active.txt", "hash", "active", "tester", now, now); err != nil {
		t.Fatal(err)
	}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	var revoked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shares WHERE status='revoked'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked != 0 {
		t.Fatalf("revoked shares remained in management storage: %d", revoked)
	}

	req := httptest.NewRequest("POST", "/share-api/v1/shares/active/revoke", nil)
	req.SetPathValue("id", "active")
	req.Header.Set("X-Auth-Request-User", "tester")
	rec := httptest.NewRecorder()
	a.revokeShare(rec, req)
	if rec.Code != 200 {
		t.Fatalf("unexpected revoke response: %d %s", rec.Code, rec.Body.String())
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM shares WHERE id='active'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("revoked share was not deleted")
	}
}

func TestEverythingDownloadPath(t *testing.T) {
	got := everythingDownloadPath(`C:\资料\hello world.txt`)
	if got != "/C%3A/%E8%B5%84%E6%96%99/hello%20world.txt" {
		t.Fatalf("unexpected download path: %s", got)
	}
}

func TestContentDispositionStripsHeaders(t *testing.T) {
	got := contentDisposition("报告\r\nInjected: yes.pdf")
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("header contains newline: %q", got)
	}
	if !strings.Contains(got, "filename*=UTF-8''") {
		t.Fatalf("missing UTF-8 filename: %q", got)
	}
}

func TestIssuedTicketSurvivesDownloadLimitButNotRevocation(t *testing.T) {
	s := shareRecord{
		Status:       "active",
		MaxDownloads: sql.NullInt64{Int64: 1, Valid: true},
		Downloads:    1,
	}
	if shareUsable(s) {
		t.Fatal("share should not issue another ticket after reaching its limit")
	}
	if !shareTicketUsable(s) {
		t.Fatal("an already issued ticket must remain usable for Range retries")
	}
	s.Status = "revoked"
	if shareTicketUsable(s) {
		t.Fatal("revocation must invalidate an issued ticket immediately")
	}
}

func TestStandaloneProxyRequiresBasicAuthAndInjectsUI(t *testing.T) {
	var upstreamAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><meta name="viewport" content="width=512"></head><body>Everything</body></html>`))
	}))
	defer upstream.Close()

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("standalone-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: config{
		EverythingBaseURL: upstream.URL,
		EverythingAuth:    "Basic upstream-credential",
		AdminKey:          strings.Repeat("a", 32),
		Standalone:        true,
		BasicAuthUsername: "admin",
		BasicAuthHash:     passwordHash,
	}}
	handler, err := a.standaloneRoutes()
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetBasicAuth("admin", "standalone-password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
	if upstreamAuthorization != "Basic upstream-credential" {
		t.Fatalf("upstream authorization was not replaced: %q", upstreamAuthorization)
	}
	if !strings.Contains(response.Body.String(), `/share-ui.js`) {
		t.Fatal("standalone proxy did not inject the sharing UI")
	}
	if !strings.Contains(response.Body.String(), `width=device-width`) {
		t.Fatal("standalone proxy did not replace the fixed viewport")
	}
}

func TestStandalonePublicSharePathsDoNotRequireBasicAuth(t *testing.T) {
	for _, requestPath := range []string{
		"/healthz",
		"/s/example",
		"/d/example",
		"/api/v1/public/shares/example/verify",
		"/assets/app.css",
	} {
		if !isPublicGatewayPath(requestPath) {
			t.Fatalf("expected public path: %s", requestPath)
		}
	}
	for _, requestPath := range []string{"/", "/share-admin/", "/share-api/v1/shares", "/main.css"} {
		if isPublicGatewayPath(requestPath) {
			t.Fatalf("expected protected path: %s", requestPath)
		}
	}
}

func TestLoadStandaloneConfigGeneratesRuntimeDefaults(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("standalone-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "everythingshare.json")
	fileConfig := standaloneFileConfig{
		ConfigVersion:         standaloneConfigVersion,
		ListenAddr:            "127.0.0.1:8088",
		PublicBaseURL:         "http://127.0.0.1:8088",
		EverythingBaseURL:     "http://127.0.0.1:8081",
		EverythingUsername:    "everything",
		EverythingPassword:    "upstream-password",
		BasicAuthUsername:     "admin",
		BasicAuthPasswordHash: string(passwordHash),
		SessionSecret:         randomEncodedBytes(32),
		AdminSharedKey:        randomEncodedBytes(36),
		DatabasePath:          filepath.Join("data", "share-gateway.db"),
		CacheDir:              "cache",
		OpenBrowser:           true,
	}
	if err := writeStandaloneConfig(configPath, fileConfig); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadStandaloneConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Standalone || cfg.BasicAuthUsername != "admin" || !cfg.OpenBrowser {
		t.Fatalf("unexpected standalone runtime config: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.DatabasePath) || !filepath.IsAbs(cfg.CacheDir) {
		t.Fatal("standalone data paths must be resolved relative to the configuration file")
	}
	if cfg.EverythingAuth != "Basic ZXZlcnl0aGluZzp1cHN0cmVhbS1wYXNzd29yZA==" {
		t.Fatal("Everything credentials were not converted to an upstream Basic header")
	}
}
