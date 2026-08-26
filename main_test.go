package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	unc, err := cleanWindowsPath(`\\server\share\资料\文件.txt`)
	if err != nil || unc != `\\server\share\资料\文件.txt` {
		t.Fatalf("unexpected UNC path: %q %v", unc, err)
	}
	for _, bad := range []string{`..\secret.txt`, `C:\safe\..\secret.txt`, `/etc/passwd`, `\\server`, `\\server\share\..\secret.txt`, `C:\safe\file.txt:stream`} {
		if _, err := cleanWindowsPath(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestSelectionArchiveBasesKeepSourcesIndependent(t *testing.T) {
	sources := []validatedSource{
		{Source: shareSource{SourcePath: `D:\家庭资料\相册`, Type: "folder", Name: "相册"}},
		{Source: shareSource{SourcePath: `D:\家庭资料\文档\年度.pdf`, Type: "file", Name: "年度.pdf"}},
	}
	bases, err := selectionArchiveBases(sources)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`相册`, `年度.pdf`}
	for i := range want {
		if bases[i] != want[i] {
			t.Fatalf("base %d = %q, want %q", i, bases[i], want[i])
		}
	}
}

func TestSelectionArchiveBasesDoNotExposeVolumes(t *testing.T) {
	sources := []validatedSource{
		{Source: shareSource{SourcePath: `C:\资料\a.txt`, Type: "file", Name: "a.txt"}},
		{Source: shareSource{SourcePath: `D:\归档\b.txt`, Type: "file", Name: "b.txt"}},
		{Source: shareSource{SourcePath: `\\nas\share\备份\c.txt`, Type: "file", Name: "c.txt"}},
	}
	bases, err := selectionArchiveBases(sources)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`a.txt`, `b.txt`, `c.txt`}
	for i := range want {
		if bases[i] != want[i] {
			t.Fatalf("base %d = %q, want %q", i, bases[i], want[i])
		}
	}
}

func TestCollapseCoveredSources(t *testing.T) {
	sources := []validatedSource{
		{Source: shareSource{SourcePath: `D:\资料\项目`, Type: "folder", Name: "项目"}},
		{Source: shareSource{SourcePath: `D:\资料\项目\文档\a.txt`, Type: "file", Name: "a.txt"}},
		{Source: shareSource{SourcePath: `D:\资料\其他.txt`, Type: "file", Name: "其他.txt"}},
	}
	got := collapseCoveredSources(sources)
	if len(got) != 2 || got[0].Source.Name != "项目" || got[1].Source.Name != "其他.txt" {
		t.Fatalf("unexpected collapsed sources: %+v", got)
	}
}

func TestValidateSourcesRejectsRootLimitAndDuplicates(t *testing.T) {
	requested := make([]shareSource, maxSelectedSources+1)
	for i := range requested {
		requested[i] = shareSource{SourcePath: fmt.Sprintf(`D:\批量\%03d.txt`, i), Type: "file"}
	}
	if _, err := (&app{}).validateSources(t.Context(), requested); err == nil {
		t.Fatal("expected the 128 root-item limit to be enforced")
	}
	items := []everythingItem{{Type: "file", Name: "same.txt", Path: `D:\资料`, SizeValue: 1, DateModified: "1"}}
	upstream := newEverythingFixture(t, items, nil)
	a := newBackendTestApp(t, upstream)
	if _, err := a.validateSources(t.Context(), []shareSource{{SourcePath: `D:\资料\same.txt`, Type: "file"}, {SourcePath: `d:\资料\same.txt`, Type: "file"}}); err == nil {
		t.Fatal("expected case-insensitive duplicate paths to be rejected")
	}
}

func TestSnapshotValidatedSourcesPreservesFolderTree(t *testing.T) {
	items := []everythingItem{
		{Type: "folder", Name: "相册", Path: `D:\家庭资料`, DateModified: "1"},
		{Type: "folder", Name: "空目录", Path: `D:\家庭资料\相册`, DateModified: "1"},
		{Type: "file", Name: "照片.jpg", Path: `D:\家庭资料\相册\2026`, SizeValue: 8, DateModified: "2"},
		{Type: "file", Name: "说明.txt", Path: `D:\家庭资料\文档`, SizeValue: 4, DateModified: "3"},
	}
	upstream := newEverythingFixture(t, items, nil)
	a := newBackendTestApp(t, upstream)
	validated, err := a.validateSources(t.Context(), []shareSource{
		{SourcePath: `D:\家庭资料\相册`, Type: "folder"},
		{SourcePath: `D:\家庭资料\文档\说明.txt`, Type: "file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := a.snapshotValidatedSources(t.Context(), validated, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]entryRecord{}
	for _, entry := range entries {
		got[entry.RelativePath] = entry
	}
	for _, relative := range []string{`相册`, `相册\空目录`, `相册\2026\照片.jpg`, `说明.txt`} {
		if _, ok := got[relative]; !ok {
			t.Fatalf("missing %q in %#v", relative, got)
		}
	}
	if got[`相册\2026\照片.jpg`].SourcePath != `D:\家庭资料\相册\2026\照片.jpg` || got[`说明.txt`].SourcePath != `D:\家庭资料\文档\说明.txt` {
		t.Fatalf("source paths were not retained: %#v", got)
	}
}

func TestSafeArchivePathRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", `..\secret.txt`, `C:\secret.txt`, `safe\..\secret.txt`, "safe\x00name"} {
		if _, err := safeArchivePath(bad); err == nil {
			t.Fatalf("expected unsafe archive path %q to fail", bad)
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

func newBackendTestApp(t *testing.T, upstream *httptest.Server) *app {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	client := http.DefaultClient
	baseURL := "http://everything.invalid"
	if upstream != nil {
		client = upstream.Client()
		baseURL = upstream.URL
	}
	a := &app{db: db, httpClient: client, cfg: config{
		PublicBaseURL:     "https://share.example",
		EverythingBaseURL: baseURL,
		EverythingAuth:    "Basic fixture",
		SessionSecret:     []byte("0123456789abcdef0123456789abcdef"),
		AdminKey:          strings.Repeat("a", 32),
		CacheDir:          t.TempDir(),
		ZipThreshold:      1 << 30,
		ZipCacheMax:       2 << 30,
		ZipTTL:            time.Hour,
	}}
	if err := a.migrate(); err != nil {
		t.Fatal(err)
	}
	return a
}

func newEverythingFixture(t *testing.T, items []everythingItem, contents map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("json") == "1" {
			_ = json.NewEncoder(w).Encode(everythingResponse{TotalResults: len(items), Results: items})
			return
		}
		for source, content := range contents {
			if r.URL.EscapedPath() == everythingDownloadPath(source) {
				_, _ = io.WriteString(w, content)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMultiSourceShareCreateAndRefresh(t *testing.T) {
	items := []everythingItem{
		{Type: "file", Name: "a.txt", Path: `D:\家庭资料`, SizeValue: 3, DateModified: "1"},
		{Type: "file", Name: "b.txt", Path: `D:\家庭资料\文档`, SizeValue: 4, DateModified: "2"},
	}
	upstream := newEverythingFixture(t, items, map[string]string{`D:\家庭资料\a.txt`: "aaa", `D:\家庭资料\文档\b.txt`: "bbbb"})
	a := newBackendTestApp(t, upstream)
	payload := `{"sources":[{"sourcePath":"D:\\家庭资料\\a.txt","type":"file"},{"sourcePath":"D:\\家庭资料\\文档\\b.txt","type":"file"}],"expiresAt":"permanent","code":"A2B4"}`
	req := httptest.NewRequest(http.MethodPost, "/share-api/v1/shares", strings.NewReader(payload))
	req.Header.Set("X-Auth-Request-User", "tester")
	rec := httptest.NewRecorder()
	a.createShare(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create share: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var shareTitle, shareName string
	if err := a.db.QueryRow(`SELECT title,source_name FROM shares WHERE id=?`, result.ID).Scan(&shareTitle, &shareName); err != nil {
		t.Fatal(err)
	}
	if shareTitle != "已选2项" || shareName != "已选2项" {
		t.Fatalf("multi-source labels = %q, %q", shareTitle, shareName)
	}
	rows, err := a.db.Query(`SELECT relative_path,source_path FROM entries WHERE share_id=? ORDER BY relative_path`, result.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]string
	for rows.Next() {
		var rel, source string
		if err := rows.Scan(&rel, &source); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{rel, source})
	}
	_ = rows.Close()
	want := [][2]string{{"a.txt", `D:\家庭资料\a.txt`}, {`b.txt`, `D:\家庭资料\文档\b.txt`}}
	if len(got) != len(want) {
		t.Fatalf("entries = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	refreshReq := httptest.NewRequest(http.MethodPost, "/share-api/v1/shares/"+result.ID+"/refresh", nil)
	refreshReq.SetPathValue("id", result.ID)
	refreshReq.Header.Set("X-Auth-Request-User", "tester")
	refreshRec := httptest.NewRecorder()
	a.refreshShare(refreshRec, refreshReq)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh share: %d %s", refreshRec.Code, refreshRec.Body.String())
	}
	var sourceCount int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE share_id=? AND source_path<>''`, result.ID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 2 {
		t.Fatalf("refreshed source paths = %d", sourceCount)
	}
}

func TestPublicMultiSourceSelectionZip(t *testing.T) {
	items := []everythingItem{
		{Type: "file", Name: "a.txt", Path: `D:\one`, SizeValue: 3, DateModified: "1"},
		{Type: "file", Name: "b.txt", Path: `D:\two`, SizeValue: 4, DateModified: "2"},
	}
	upstream := newEverythingFixture(t, items, map[string]string{`D:\one\a.txt`: "aaa", `D:\two\b.txt`: "bbbb"})
	a := newBackendTestApp(t, upstream)
	create := httptest.NewRequest(http.MethodPost, "/share-api/v1/shares", strings.NewReader(`{"sources":[{"sourcePath":"D:\\one\\a.txt","type":"file"},{"sourcePath":"D:\\two\\b.txt","type":"file"}],"expiresAt":"permanent","code":"A2B4"}`))
	create.Header.Set("X-Auth-Request-User", "tester")
	created := httptest.NewRecorder()
	a.createShare(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create share: %d %s", created.Code, created.Body.String())
	}
	var shareResult struct {
		ID      string `json:"id"`
		BaseURL string `json:"baseUrl"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &shareResult); err != nil {
		t.Fatal(err)
	}
	token := shareResult.BaseURL[strings.LastIndex(shareResult.BaseURL, "/")+1:]
	cookie := &http.Cookie{Name: a.guestCookieName(token), Value: a.signSession(shareResult.ID, time.Now().Add(time.Hour).Unix())}

	entriesReq := httptest.NewRequest(http.MethodGet, "/api/v1/public/shares/"+token+"/entries", nil)
	entriesReq.SetPathValue("token", token)
	entriesReq.AddCookie(cookie)
	entriesRec := httptest.NewRecorder()
	a.publicEntries(entriesRec, entriesReq)
	if entriesRec.Code != http.StatusOK || !strings.Contains(entriesRec.Body.String(), `"name":"a.txt"`) || !strings.Contains(entriesRec.Body.String(), `"name":"b.txt"`) {
		t.Fatalf("independent public roots: %d %s", entriesRec.Code, entriesRec.Body.String())
	}

	var selectedID string
	if err := a.db.QueryRow(`SELECT id FROM entries WHERE share_id=? AND name='b.txt'`, shareResult.ID).Scan(&selectedID); err != nil {
		t.Fatal(err)
	}
	downloadReq := httptest.NewRequest(http.MethodPost, "/api/v1/public/shares/"+token+"/downloads", strings.NewReader(fmt.Sprintf(`{"zip":true,"entryIds":[%q]}`, selectedID)))
	downloadReq.SetPathValue("token", token)
	downloadReq.AddCookie(cookie)
	downloadRec := httptest.NewRecorder()
	a.createDownload(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusCreated {
		t.Fatalf("create selected download: %d %s", downloadRec.Code, downloadRec.Body.String())
	}
	var downloadResult struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(downloadRec.Body.Bytes(), &downloadResult); err != nil {
		t.Fatal(err)
	}
	ticket := downloadResult.URL[strings.LastIndex(downloadResult.URL, "/")+1:]
	zipReq := httptest.NewRequest(http.MethodGet, "/d/"+ticket, nil)
	zipReq.SetPathValue("ticket", ticket)
	zipRec := httptest.NewRecorder()
	a.download(zipRec, zipReq)
	reader, err := zip.NewReader(bytes.NewReader(zipRec.Body.Bytes()), int64(zipRec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "b.txt" {
		t.Fatalf("selected zip entries = %+v", reader.File)
	}
}

func TestLegacyShareRequestAndEntryFallback(t *testing.T) {
	items := []everythingItem{{Type: "file", Name: "legacy.txt", Path: `C:\资料`, SizeValue: 6, DateModified: "1"}}
	upstream := newEverythingFixture(t, items, map[string]string{`C:\资料\legacy.txt`: "legacy"})
	a := newBackendTestApp(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/share-api/v1/shares", strings.NewReader(`{"sourcePath":"C:\\资料\\legacy.txt","type":"file","expiresAt":"permanent","code":"A2B4"}`))
	req.Header.Set("X-Auth-Request-User", "tester")
	rec := httptest.NewRecorder()
	a.createShare(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("legacy create: %d %s", rec.Code, rec.Body.String())
	}
	var stored string
	if err := a.db.QueryRow(`SELECT sources_json FROM shares LIMIT 1`).Scan(&stored); err != nil || !strings.Contains(stored, "legacy.txt") {
		t.Fatalf("legacy sources were not stored: %q %v", stored, err)
	}
	now := time.Now().Unix()
	if _, err := a.db.Exec(`INSERT INTO shares(id,token,title,source_path,source_type,source_name,code_hash,status,created_by,created_at,updated_at,entry_count,total_size) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"folder", "folder-token", "folder", `C:\资料`, "folder", "资料", "hash", "active", "tester", now, now, 1, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO entries(id,share_id,relative_path,parent_path,name,kind,size,modified,source_path) VALUES(?,?,?,?,?,?,?,?,?)`,
		"entry", "folder", "legacy.txt", "", "legacy.txt", "file", 6, "1", ""); err != nil {
		t.Fatal(err)
	}
	share, err := a.loadShareByID(t.Context(), "folder")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := a.loadShareArchiveEntries(t.Context(), share)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].SourcePath != `C:\资料\legacy.txt` {
		t.Fatalf("legacy fallback entries: %+v", entries)
	}
}

func TestDownloadPackageCachedZipAndExpiry(t *testing.T) {
	items := []everythingItem{
		{Type: "file", Name: "a.txt", Path: `D:\家庭资料`, SizeValue: 3, DateModified: "1"},
		{Type: "file", Name: "b.txt", Path: `D:\家庭资料\文档`, SizeValue: 4, DateModified: "2"},
	}
	upstream := newEverythingFixture(t, items, map[string]string{`D:\家庭资料\a.txt`: "aaa", `D:\家庭资料\文档\b.txt`: "bbbb"})
	a := newBackendTestApp(t, upstream)
	handler := a.routes()
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/share-api/v1/download-packages", strings.NewReader(`{"sources":[]}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized package create = %d", unauthorized.Code)
	}
	payload := `{"sources":[{"sourcePath":"D:\\家庭资料\\a.txt","type":"file"},{"sourcePath":"D:\\家庭资料\\文档\\b.txt","type":"file"}]}`
	request := httptest.NewRequest(http.MethodPost, "/share-api/v1/download-packages", strings.NewReader(payload))
	request.Header.Set("X-Share-Admin-Key", a.cfg.AdminKey)
	request.Header.Set("X-Auth-Request-User", "tester")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("package create: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		ID      string `json:"id"`
		ZipMode string `json:"zipMode"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ZipMode != "cached" {
		t.Fatalf("zip mode = %q", result.ZipMode)
	}
	var packageName string
	if err := a.db.QueryRow(`SELECT filename FROM download_packages WHERE id=?`, result.ID).Scan(&packageName); err != nil || packageName != "已选2项.zip" {
		t.Fatalf("package filename = %q (%v)", packageName, err)
	}
	unauthorizedDownload := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedDownload, httptest.NewRequest(http.MethodGet, "/share-api/v1/download-packages/"+result.ID, nil))
	if unauthorizedDownload.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized package download = %d", unauthorizedDownload.Code)
	}
	downloadReq := httptest.NewRequest(http.MethodGet, "/share-api/v1/download-packages/"+result.ID, nil)
	downloadReq.Header.Set("X-Share-Admin-Key", a.cfg.AdminKey)
	downloadReq.Header.Set("X-Auth-Request-User", "tester")
	downloadRec := httptest.NewRecorder()
	handler.ServeHTTP(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusOK {
		t.Fatalf("package download: %d %s", downloadRec.Code, downloadRec.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(downloadRec.Body.Bytes()), int64(downloadRec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[file.Name] = string(data)
	}
	if contents["a.txt"] != "aaa" || contents["b.txt"] != "bbbb" {
		t.Fatalf("zip contents = %#v", contents)
	}
	if _, err := a.db.Exec(`UPDATE download_packages SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), result.ID); err != nil {
		t.Fatal(err)
	}
	expiredReq := httptest.NewRequest(http.MethodGet, "/share-api/v1/download-packages/"+result.ID, nil)
	expiredReq.Header.Set("X-Share-Admin-Key", a.cfg.AdminKey)
	expiredReq.Header.Set("X-Auth-Request-User", "tester")
	expiredRec := httptest.NewRecorder()
	handler.ServeHTTP(expiredRec, expiredReq)
	if expiredRec.Code != http.StatusGone {
		t.Fatalf("expired package = %d %s", expiredRec.Code, expiredRec.Body.String())
	}
	if _, err := os.Stat(a.packageCachePath(result.ID)); !os.IsNotExist(err) {
		t.Fatalf("expired package cache still exists: %v", err)
	}
	var remaining int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM download_packages WHERE id=?`, result.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired package record remains: %d %v", remaining, err)
	}
}

func TestDownloadPackageStreamingZip(t *testing.T) {
	items := []everythingItem{{Type: "file", Name: "large.bin", Path: `E:\数据`, SizeValue: 5, DateModified: "1"}}
	upstream := newEverythingFixture(t, items, map[string]string{`E:\数据\large.bin`: "12345"})
	a := newBackendTestApp(t, upstream)
	a.cfg.ZipThreshold = 1
	req := httptest.NewRequest(http.MethodPost, "/share-api/v1/download-packages", strings.NewReader(`{"sources":[{"sourcePath":"E:\\数据\\large.bin","type":"file"}]}`))
	req.Header.Set("X-Auth-Request-User", "tester")
	rec := httptest.NewRecorder()
	a.createDownloadPackage(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("stream package create: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		ID      string `json:"id"`
		ZipMode string `json:"zipMode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ZipMode != "stream" {
		t.Fatalf("zip mode = %q", result.ZipMode)
	}
	downloadReq := httptest.NewRequest(http.MethodGet, "/share-api/v1/download-packages/"+result.ID, nil)
	downloadReq.SetPathValue("ticket", result.ID)
	downloadReq.Header.Set("X-Auth-Request-User", "tester")
	downloadRec := httptest.NewRecorder()
	a.downloadPackage(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusOK || downloadRec.Header().Get("X-Zip-Mode") != "stream" {
		t.Fatalf("stream package download: %d %s", downloadRec.Code, downloadRec.Body.String())
	}
	if _, err := zip.NewReader(bytes.NewReader(downloadRec.Body.Bytes()), int64(downloadRec.Body.Len())); err != nil {
		t.Fatal(err)
	}
}

func TestWriteArchivePreservesEmptyFolders(t *testing.T) {
	items := []everythingItem{{Type: "file", Name: "file.txt", Path: `D:\来源`, SizeValue: 4, DateModified: "1"}}
	upstream := newEverythingFixture(t, items, map[string]string{`D:\来源\file.txt`: "data"})
	a := newBackendTestApp(t, upstream)
	entries := []entryRecord{
		{RelativePath: `根目录\空文件夹`, Kind: "folder", SourcePath: `D:\来源\空文件夹`},
		{RelativePath: `根目录\file.txt`, Kind: "file", SourcePath: `D:\来源\file.txt`, Size: 4, Modified: "1"},
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := a.writeArchive(t.Context(), writer, entries); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	if len(names) != 2 || names[0] != "根目录/空文件夹/" || names[1] != "根目录/file.txt" {
		t.Fatalf("archive entries = %#v", names)
	}
}

func TestWriteArchiveRejectsChangedSource(t *testing.T) {
	items := []everythingItem{{Type: "file", Name: "file.txt", Path: `D:\来源`, SizeValue: 4, DateModified: "new"}}
	upstream := newEverythingFixture(t, items, map[string]string{`D:\来源\file.txt`: "data"})
	a := newBackendTestApp(t, upstream)
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	err := a.writeArchive(t.Context(), writer, []entryRecord{{
		RelativePath: "file.txt", Kind: "file", SourcePath: `D:\来源\file.txt`, Size: 4, Modified: "old",
	}})
	_ = writer.Close()
	if err == nil {
		t.Fatal("expected changed source to stop ZIP generation")
	}
}

func TestEverythingDownloadPath(t *testing.T) {
	got := everythingDownloadPath(`C:\资料\hello world.txt`)
	if got != "/C%3A/%E8%B5%84%E6%96%99/hello%20world.txt" {
		t.Fatalf("unexpected download path: %s", got)
	}
	if got := everythingDownloadPath(`\\server\share\文件.txt`); got != "//server/share/%E6%96%87%E4%BB%B6.txt" {
		t.Fatalf("unexpected UNC download path: %s", got)
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
	if !isStandaloneAssetPath("/qrcode.js") {
		t.Fatal("local QR generator is not exposed as a protected standalone asset")
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
