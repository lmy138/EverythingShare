package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const standaloneConfigVersion = 1

var version = "dev"

//go:embed everything-ui/main.css everything-ui/share-ui.js
var everythingUIFiles embed.FS

type standaloneFileConfig struct {
	ConfigVersion          int    `json:"config_version"`
	ListenAddr             string `json:"listen_addr"`
	PublicBaseURL          string `json:"public_base_url"`
	EverythingBaseURL      string `json:"everything_base_url"`
	EverythingUsername     string `json:"everything_username"`
	EverythingPassword     string `json:"everything_password"`
	BasicAuthUsername      string `json:"basic_auth_username"`
	BasicAuthPasswordHash  string `json:"basic_auth_password_hash"`
	SessionSecret          string `json:"session_secret"`
	AdminSharedKey         string `json:"admin_shared_key"`
	DatabasePath           string `json:"database_path"`
	CacheDir               string `json:"cache_dir"`
	CookieSecure           bool   `json:"cookie_secure"`
	TrustProxyHeaders      bool   `json:"trust_proxy_headers"`
	OpenBrowser            bool   `json:"open_browser"`
	ZipCacheThresholdBytes int64  `json:"zip_cache_threshold_bytes"`
	ZipCacheMaxBytes       int64  `json:"zip_cache_max_bytes"`
	ZipCacheTTLHours       int64  `json:"zip_cache_ttl_hours"`
	ZipCacheMinFreeBytes   int64  `json:"zip_cache_min_free_bytes"`
}

func handleStandaloneCommand(args []string) (bool, error) {
	command := ""
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			command = strings.ToLower(arg)
			break
		}
	}
	switch command {
	case "setup":
		path := standaloneConfigPath(args)
		force := containsArg(args, "--force")
		if err := runStandaloneWizard(path, force); err != nil {
			return true, err
		}
		fmt.Printf("\n配置已保存到 %s\n双击 EverythingShare.exe 即可启动。\n", path)
		return true, nil
	case "version":
		fmt.Printf("EverythingShare %s\n", version)
		return true, nil
	case "healthcheck", "backup", "":
		if containsArg(args, "--help") || containsArg(args, "-h") {
			printStandaloneUsage()
			return true, nil
		}
		return false, nil
	default:
		return false, nil
	}
}

func printStandaloneUsage() {
	fmt.Println(`EverythingShare

用法:
  EverythingShare.exe                 启动；首次运行时自动进入配置向导
  EverythingShare.exe setup           重新运行配置向导
  EverythingShare.exe setup --force   覆盖现有配置
  EverythingShare.exe --config PATH   使用指定配置文件
  EverythingShare.exe version         显示版本`)
}

func containsArg(args []string, expected string) bool {
	for _, arg := range args {
		if strings.EqualFold(arg, expected) {
			return true
		}
	}
	return false
}

func hasGatewayEnvironment() bool {
	for _, name := range []string{"SESSION_SECRET", "ADMIN_SHARED_KEY", "PUBLIC_BASE_URL", "EVERYTHING_AUTH_HEADER"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func standaloneConfigPath(args []string) string {
	for index, arg := range args {
		if strings.EqualFold(arg, "--config") && index+1 < len(args) {
			if absolute, err := filepath.Abs(args[index+1]); err == nil {
				return absolute
			}
			return args[index+1]
		}
		if strings.HasPrefix(strings.ToLower(arg), "--config=") {
			value := strings.TrimSpace(arg[len("--config="):])
			if absolute, err := filepath.Abs(value); err == nil {
				return absolute
			}
			return value
		}
	}
	executable, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(executable), "everythingshare.json")
	}
	return "everythingshare.json"
}

func loadStandaloneConfig(configPath string) (config, error) {
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		info, statErr := os.Stdin.Stat()
		if statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
			return config{}, fmt.Errorf("configuration file not found: %s; run EverythingShare.exe setup", configPath)
		}
		fmt.Println("未找到配置文件，正在启动首次运行向导。")
		if err := runStandaloneWizard(configPath, false); err != nil {
			return config{}, err
		}
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return config{}, fmt.Errorf("read standalone configuration: %w", err)
	}
	var fileConfig standaloneFileConfig
	if err := json.Unmarshal(raw, &fileConfig); err != nil {
		return config{}, fmt.Errorf("parse standalone configuration: %w", err)
	}
	if fileConfig.ConfigVersion != standaloneConfigVersion {
		return config{}, fmt.Errorf("unsupported configuration version %d", fileConfig.ConfigVersion)
	}
	if err := validateStandaloneFileConfig(fileConfig); err != nil {
		return config{}, err
	}
	session, err := decodeSecret(fileConfig.SessionSecret)
	if err != nil || len(session) < 32 {
		return config{}, errors.New("session_secret must contain at least 32 random bytes")
	}
	configDir := filepath.Dir(configPath)
	databasePath := resolveConfigPath(configDir, fileConfig.DatabasePath)
	cacheDir := resolveConfigPath(configDir, fileConfig.CacheDir)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(fileConfig.EverythingUsername+":"+fileConfig.EverythingPassword))
	return config{
		ListenAddr:        fileConfig.ListenAddr,
		PublicBaseURL:     strings.TrimRight(fileConfig.PublicBaseURL, "/"),
		EverythingBaseURL: strings.TrimRight(fileConfig.EverythingBaseURL, "/"),
		EverythingAuth:    auth,
		SessionSecret:     session,
		AdminKey:          fileConfig.AdminSharedKey,
		DatabasePath:      databasePath,
		CacheDir:          cacheDir,
		CookieSecure:      fileConfig.CookieSecure,
		TrustProxyHeaders: fileConfig.TrustProxyHeaders,
		ZipThreshold:      positiveOrDefault(fileConfig.ZipCacheThresholdBytes, 5<<30),
		ZipCacheMax:       positiveOrDefault(fileConfig.ZipCacheMaxBytes, 50<<30),
		ZipTTL:            time.Duration(positiveOrDefault(fileConfig.ZipCacheTTLHours, 24)) * time.Hour,
		ZipMinFree:        uint64(positiveOrDefault(fileConfig.ZipCacheMinFreeBytes, 20<<30)),
		Standalone:        true,
		BasicAuthUsername: fileConfig.BasicAuthUsername,
		BasicAuthHash:     []byte(fileConfig.BasicAuthPasswordHash),
		OpenBrowser:       fileConfig.OpenBrowser,
	}, nil
}

func resolveConfigPath(configDir, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(configDir, filepath.Clean(value))
}

func positiveOrDefault(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func validateStandaloneFileConfig(fileConfig standaloneFileConfig) error {
	if _, _, err := net.SplitHostPort(fileConfig.ListenAddr); err != nil {
		return errors.New("listen_addr must use host:port format")
	}
	if err := validateBaseURL("public_base_url", strings.TrimSpace(fileConfig.PublicBaseURL)); err != nil {
		return err
	}
	if err := validateBaseURL("everything_base_url", strings.TrimSpace(fileConfig.EverythingBaseURL)); err != nil {
		return err
	}
	if fileConfig.EverythingUsername == "" || fileConfig.EverythingPassword == "" {
		return errors.New("Everything HTTP username and password are required")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(fileConfig.BasicAuthUsername) {
		return errors.New("basic_auth_username contains unsupported characters")
	}
	if _, err := bcrypt.Cost([]byte(fileConfig.BasicAuthPasswordHash)); err != nil {
		return errors.New("basic_auth_password_hash is invalid; rerun setup")
	}
	if len(fileConfig.AdminSharedKey) < 32 {
		return errors.New("admin_shared_key must be at least 32 characters")
	}
	if fileConfig.DatabasePath == "" || fileConfig.CacheDir == "" {
		return errors.New("database_path and cache_dir are required")
	}
	return nil
}

func runStandaloneWizard(configPath string, force bool) error {
	if _, err := os.Stat(configPath); err == nil && !force {
		return fmt.Errorf("%s already exists; use setup --force only when you intend to replace it", configPath)
	}
	fmt.Println()
	fmt.Println("EverythingShare Windows 快速配置向导")
	fmt.Println("这会创建本地 BasicAuth 体验环境，不需要 Docker。")
	fmt.Println("请先在 Everything 中启用 HTTP Server、账号密码和文件下载。")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	everythingURL, err := promptValue(reader, "Everything HTTP 地址", "http://127.0.0.1:8081")
	if err != nil {
		return err
	}
	if err := validateBaseURL("Everything HTTP 地址", everythingURL); err != nil {
		return err
	}
	everythingUsername, err := promptValue(reader, "Everything HTTP 用户名", "admin")
	if err != nil {
		return err
	}
	everythingPassword, err := readConsoleSecret("Everything HTTP 密码: ")
	if err != nil {
		return err
	}
	if everythingPassword == "" {
		return errors.New("Everything HTTP 密码不能为空")
	}
	listenAddr, err := promptValue(reader, "本地监听地址", "127.0.0.1:8088")
	if err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(listenAddr); err != nil {
		return errors.New("本地监听地址必须使用 IP:端口 格式，例如 127.0.0.1:8088")
	}
	defaultPublicURL := defaultURLForListenAddr(listenAddr)
	publicBaseURL, err := promptValue(reader, "分享链接基础地址", defaultPublicURL)
	if err != nil {
		return err
	}
	if err := validateBaseURL("分享链接基础地址", publicBaseURL); err != nil {
		return err
	}
	basicUsername, err := promptValue(reader, "EverythingShare 登录用户名", "admin")
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(basicUsername) {
		return errors.New("登录用户名只能包含字母、数字、点、下划线和短横线")
	}
	basicPassword, err := readConsoleSecret("EverythingShare 登录密码（至少 10 位）: ")
	if err != nil {
		return err
	}
	if len([]rune(basicPassword)) < 10 {
		return errors.New("EverythingShare 登录密码至少需要 10 位")
	}
	confirmPassword, err := readConsoleSecret("再次输入 EverythingShare 登录密码: ")
	if err != nil {
		return err
	}
	if basicPassword != confirmPassword {
		return errors.New("两次输入的 EverythingShare 登录密码不一致")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(basicPassword), 12)
	if err != nil {
		return fmt.Errorf("hash BasicAuth password: %w", err)
	}
	basicPassword = ""
	confirmPassword = ""

	fileConfig := standaloneFileConfig{
		ConfigVersion:          standaloneConfigVersion,
		ListenAddr:             listenAddr,
		PublicBaseURL:          strings.TrimRight(publicBaseURL, "/"),
		EverythingBaseURL:      strings.TrimRight(everythingURL, "/"),
		EverythingUsername:     everythingUsername,
		EverythingPassword:     everythingPassword,
		BasicAuthUsername:      basicUsername,
		BasicAuthPasswordHash:  string(passwordHash),
		SessionSecret:          randomEncodedBytes(32),
		AdminSharedKey:         randomEncodedBytes(36),
		DatabasePath:           filepath.Join("data", "share-gateway.db"),
		CacheDir:               "cache",
		CookieSecure:           strings.HasPrefix(strings.ToLower(publicBaseURL), "https://"),
		TrustProxyHeaders:      false,
		OpenBrowser:            true,
		ZipCacheThresholdBytes: 5 << 30,
		ZipCacheMaxBytes:       50 << 30,
		ZipCacheTTLHours:       24,
		ZipCacheMinFreeBytes:   20 << 30,
	}
	if err := writeStandaloneConfig(configPath, fileConfig); err != nil {
		return err
	}
	if err := probeEverything(fileConfig); err != nil {
		fmt.Printf("\n提示：配置已保存，但暂时无法连接 Everything：%v\n", err)
		fmt.Println("启动 Everything HTTP Server 后再次双击 EverythingShare.exe 即可。")
	} else {
		fmt.Println("\nEverything 连接测试通过。")
	}
	return nil
}

func promptValue(reader *bufio.Reader, label, fallback string) (string, error) {
	if fallback == "" {
		fmt.Printf("%s: ", label)
	} else {
		fmt.Printf("%s [%s]: ", label, fallback)
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	return value, nil
}

func defaultURLForListenAddr(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "http://127.0.0.1:8088"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func randomEncodedBytes(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func writeStandaloneConfig(configPath string, fileConfig standaloneFileConfig) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(fileConfig, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		return fmt.Errorf("write standalone configuration: %w", err)
	}
	if err := protectConfigFile(configPath); err != nil {
		log.Printf("warning: could not tighten configuration ACL: %v", err)
	}
	return nil
}

func probeEverything(fileConfig standaloneFileConfig) error {
	ctxClient := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(fileConfig.EverythingBaseURL, "/")+"/?json=1&count=0", nil)
	if err != nil {
		return err
	}
	request.SetBasicAuth(fileConfig.EverythingUsername, fileConfig.EverythingPassword)
	response, err := ctxClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func (a *app) standaloneRoutes() (http.Handler, error) {
	target, err := url.Parse(a.cfg.EverythingBaseURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Header.Del("X-Share-Admin-Key")
		request.Header.Del("X-Auth-Request-User")
		request.Header.Set("Authorization", a.cfg.EverythingAuth)
		request.Header.Set("Accept-Encoding", "")
	}
	proxy.ModifyResponse = injectEverythingUI
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
		log.Printf("Everything proxy error: %v", proxyErr)
		http.Error(w, "Everything HTTP Server is unavailable", http.StatusBadGateway)
	}

	gateway := a.routes()
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case isPublicGatewayPath(request.URL.Path):
			gateway.ServeHTTP(w, request)
			return
		case isStandaloneAssetPath(request.URL.Path):
			if !a.requireBasicAuth(w, request) {
				return
			}
			serveEverythingUIAsset(w, request)
			return
		case isAdminGatewayPath(request.URL.Path):
			username, ok := a.basicAuthUser(w, request)
			if !ok {
				return
			}
			adminRequest := request.Clone(request.Context())
			adminRequest.Header = request.Header.Clone()
			adminRequest.Header.Set("X-Share-Admin-Key", a.cfg.AdminKey)
			adminRequest.Header.Set("X-Auth-Request-User", username)
			gateway.ServeHTTP(w, adminRequest)
			return
		default:
			if !a.requireBasicAuth(w, request) {
				return
			}
			proxy.ServeHTTP(w, request)
		}
	}), nil
}

func isPublicGatewayPath(requestPath string) bool {
	return requestPath == "/healthz" ||
		strings.HasPrefix(requestPath, "/s/") ||
		strings.HasPrefix(requestPath, "/d/") ||
		strings.HasPrefix(requestPath, "/api/v1/public/") ||
		strings.HasPrefix(requestPath, "/assets/")
}

func isAdminGatewayPath(requestPath string) bool {
	return requestPath == "/share-admin" ||
		strings.HasPrefix(requestPath, "/share-admin/") ||
		strings.HasPrefix(requestPath, "/share-api/")
}

func isStandaloneAssetPath(requestPath string) bool {
	return requestPath == "/main.css" || requestPath == "/share-ui.js"
}

func (a *app) requireBasicAuth(w http.ResponseWriter, request *http.Request) bool {
	_, ok := a.basicAuthUser(w, request)
	return ok
}

func (a *app) basicAuthUser(w http.ResponseWriter, request *http.Request) (string, bool) {
	username, password, ok := request.BasicAuth()
	usernameMatch := subtleStringEqual(username, a.cfg.BasicAuthUsername)
	passwordMatch := bcrypt.CompareHashAndPassword(a.cfg.BasicAuthHash, []byte(password)) == nil
	if !ok || !usernameMatch || !passwordMatch {
		w.Header().Set("WWW-Authenticate", `Basic realm="EverythingShare", charset="UTF-8"`)
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return "", false
	}
	return username, true
}

func subtleStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func serveEverythingUIAsset(w http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(request.URL.Path, "/")
	raw, err := fs.ReadFile(everythingUIFiles, "everything-ui/"+name)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	switch filepath.Ext(name) {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(raw)
}

func injectEverythingUI(response *http.Response) error {
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	raw = bytes.ReplaceAll(raw,
		[]byte(`<meta name="viewport" content="width=512">`),
		[]byte(`<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">`))
	if !bytes.Contains(raw, []byte(`/share-ui.js`)) {
		raw = bytes.Replace(raw, []byte(`</body>`), []byte(`<script src="/share-ui.js" defer></script></body>`), 1)
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	response.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	response.Header.Del("Content-Encoding")
	return nil
}
