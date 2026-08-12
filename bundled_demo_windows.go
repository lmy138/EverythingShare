//go:build windows

package main

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	bundledEverythingVersion = "1.4.1.1032"
	bundledEverythingSHA256  = "f191f756996a14a11e5445fa7103d302efd510cf2fbf920e6c0c8ed51d512e36"
)

// The release workflow replaces the tracked placeholder with the verified
// official portable executable before compiling EverythingShare-Demo.exe.
//
//go:embed third_party/everything/Everything.exe.bin third_party/everything/License.txt third_party/everything/SOURCE.txt
var bundledEverythingFiles embed.FS

type demoRuntime struct {
	root               string
	everythingURL      string
	everythingUsername string
	everythingPassword string
	listenAddr         string
	publicBaseURL      string
	state              demoState
}

type demoState struct {
	SessionSecret  string `json:"session_secret"`
	AdminSharedKey string `json:"admin_shared_key"`
	HTTPUsername   string `json:"http_username"`
	HTTPPassword   string `json:"http_password"`
	InstanceName   string `json:"instance_name"`
}

var bundledDemoRuntime *demoRuntime

func prepareBundledDemo() (func(), error) {
	if edition != "demo" {
		return nil, nil
	}
	for _, arg := range os.Args[1:] {
		if strings.EqualFold(arg, "version") || strings.EqualFold(arg, "--help") || strings.EqualFold(arg, "-h") {
			return nil, nil
		}
	}
	payload, err := bundledEverythingFiles.ReadFile("third_party/everything/Everything.exe.bin")
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	if len(payload) < 2 || payload[0] != 'M' || payload[1] != 'Z' || hex.EncodeToString(digest[:]) != bundledEverythingSHA256 {
		return nil, errors.New("this binary does not contain the verified Everything portable payload")
	}

	root, err := demoDataRoot()
	if err != nil {
		return nil, err
	}
	everythingDir := filepath.Join(root, "Everything")
	demoFiles := filepath.Join(root, "Demo Files")
	for _, directory := range []string{everythingDir, demoFiles, filepath.Join(root, "data"), filepath.Join(root, "cache")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, err
		}
	}
	if err := createDemoFiles(demoFiles); err != nil {
		return nil, err
	}
	state, err := loadOrCreateDemoState(filepath.Join(root, "demo-state.json"))
	if err != nil {
		return nil, err
	}
	everythingExe := filepath.Join(everythingDir, "Everything.exe")
	if err := writeVerifiedPayload(everythingExe, payload); err != nil {
		return nil, err
	}
	for _, name := range []string{"License.txt", "SOURCE.txt"} {
		content, readErr := bundledEverythingFiles.ReadFile("third_party/everything/" + name)
		if readErr != nil {
			return nil, readErr
		}
		if err := os.WriteFile(filepath.Join(everythingDir, name), content, 0600); err != nil {
			return nil, err
		}
	}

	everythingPort, err := reserveLocalPort(18081)
	if err != nil {
		return nil, err
	}
	sharePort, err := reserveLocalPort(18088)
	if err != nil {
		return nil, err
	}
	instance := state.InstanceName
	configPath := filepath.Join(everythingDir, "Everything-"+instance+".ini")
	if err := writeDemoEverythingINI(configPath, demoFiles, everythingPort, state); err != nil {
		return nil, err
	}

	exitCmd := exec.Command(everythingExe, "-instance", instance, "-exit")
	exitCmd.Dir = everythingDir
	_ = exitCmd.Run()
	cmd := exec.Command(everythingExe, "-instance", instance, "-noapp-data", "-language", "1033")
	cmd.Dir = everythingDir
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bundled Everything: %w", err)
	}
	everythingURL := fmt.Sprintf("http://127.0.0.1:%d", everythingPort)
	if err := waitForDemoEverything(everythingURL, state, 25*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("%w; please run the demo from an interactive Windows desktop session", err)
	}
	bundledDemoRuntime = &demoRuntime{
		root: root, everythingURL: everythingURL,
		everythingUsername: state.HTTPUsername, everythingPassword: state.HTTPPassword,
		listenAddr:    fmt.Sprintf("127.0.0.1:%d", sharePort),
		publicBaseURL: fmt.Sprintf("http://127.0.0.1:%d", sharePort), state: state,
	}
	return func() {
		stop := exec.Command(everythingExe, "-instance", instance, "-exit")
		stop.Dir = everythingDir
		_ = stop.Run()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}, nil
}

func loadBundledDemoConfig(runtime demoRuntime) (config, error) {
	session, err := decodeSecret(runtime.state.SessionSecret)
	if err != nil || len(session) < 32 {
		return config{}, errors.New("invalid generated demo session secret")
	}
	return config{
		ListenAddr: runtime.listenAddr, PublicBaseURL: runtime.publicBaseURL,
		EverythingBaseURL: runtime.everythingURL,
		EverythingAuth:    "Basic " + base64Basic(runtime.everythingUsername, runtime.everythingPassword),
		SessionSecret:     session, AdminKey: runtime.state.AdminSharedKey,
		DatabasePath: filepath.Join(runtime.root, "data", "share-gateway.db"),
		CacheDir:     filepath.Join(runtime.root, "cache"), CookieSecure: false,
		TrustProxyHeaders: false, ZipThreshold: 5 << 30, ZipCacheMax: 10 << 30,
		ZipTTL: 24 * time.Hour, ZipMinFree: 256 << 20,
		Standalone: true, OpenBrowser: !containsArg(os.Args[1:], "--no-browser"), DemoMode: true,
	}, nil
}

func base64Basic(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

func demoDataRoot() (string, error) {
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(base, "EverythingShare Demo"), nil
}

func reserveLocalPort(preferred int) (int, error) {
	for _, address := range []string{fmt.Sprintf("127.0.0.1:%d", preferred), "127.0.0.1:0"} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			continue
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		return port, nil
	}
	return 0, errors.New("unable to reserve a local port")
}

func loadOrCreateDemoState(path string) (demoState, error) {
	if raw, err := os.ReadFile(path); err == nil {
		var state demoState
		if json.Unmarshal(raw, &state) == nil && len(state.AdminSharedKey) >= 32 && state.HTTPUsername != "" && state.HTTPPassword != "" {
			if state.InstanceName == "" || state.InstanceName == "EverythingShareDemo" {
				state.InstanceName = "EverythingShareDemo-" + randomID(6)
				if err := saveDemoState(path, state); err != nil {
					return demoState{}, err
				}
			}
			return state, nil
		}
	}
	state := demoState{
		SessionSecret: randomEncodedBytes(32), AdminSharedKey: randomEncodedBytes(36),
		HTTPUsername: "everythingshare-demo", HTTPPassword: randomEncodedBytes(24),
		InstanceName: "EverythingShareDemo-" + randomID(6),
	}
	if err := saveDemoState(path, state); err != nil {
		return demoState{}, err
	}
	return state, nil
}

func saveDemoState(path string, state demoState) error {
	raw, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		return err
	}
	_ = protectConfigFile(path)
	return nil
}

func writeVerifiedPayload(path string, payload []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		digest := sha256.Sum256(existing)
		if hex.EncodeToString(digest[:]) == bundledEverythingSHA256 {
			return nil
		}
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0700); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return nil
}

func writeDemoEverythingINI(path, demoFiles string, port int, state demoState) error {
	escapedFolder := strings.ReplaceAll(demoFiles, ",", `\,`)
	content := fmt.Sprintf(`[Everything]
app_data=0
run_as_admin=0
allow_multiple_instances=1
instance_name=%s
allow_http_server=1
http_server_enabled=1
http_server_bindings=127.0.0.1
http_server_port=%d
http_server_username=%s
http_server_password=%s
http_server_allow_file_download=1
http_server_items_per_page=1000
http_server_logging_enabled=0
auto_include_fixed_volumes=0
auto_include_removable_volumes=0
auto_include_fixed_refs_volumes=0
auto_include_removable_refs_volumes=0
ntfs_volume_guids=
ntfs_volume_paths=
ntfs_volume_roots=
ntfs_volume_includes=
ntfs_volume_include_onlys=
ntfs_volume_monitors=
refs_volume_guids=
refs_volume_paths=
refs_volume_roots=
refs_volume_includes=
refs_volume_include_onlys=
refs_volume_monitors=
folders=%s
folder_monitor_changes=1
folder_update_types=0
folder_update_days=0
folder_update_ats=0
folder_update_intervals=15
folder_update_interval_types=0
show_tray_icon=0
check_for_updates_on_startup=0
`, state.InstanceName, port, state.HTTPUsername, state.HTTPPassword, escapedFolder)
	return os.WriteFile(path, []byte(content), 0600)
}

func createDemoFiles(root string) error {
	files := map[string]string{
		"欢迎使用 EverythingShare.txt":                      "EverythingShare 一键 Demo\r\n\r\n你可以搜索、创建分享、复制带提取码的链接并测试下载。\r\n",
		filepath.Join("家庭资料", "旅行计划.md"):                "# 家庭旅行计划\r\n\r\n这是虚构的 Demo 文件，不包含真实个人信息。\r\n",
		filepath.Join("家庭资料", "家庭预算示例.csv"):             "项目,预算\r\n交通,1200\r\n住宿,2400\r\n餐饮,800\r\n",
		filepath.Join("项目文件", "EverythingShare-说明.txt"): "右侧三点菜单可以创建分享或复制完整路径。\r\n",
		filepath.Join("照片示例", "README.txt"):             "此目录用于演示文件夹分享、浏览和 ZIP 下载。\r\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitForDemoEverything(baseURL string, state demoState, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	lastResult := "no response"
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, baseURL+"/?json=1&search="+url.QueryEscape("欢迎使用 EverythingShare.txt"), nil)
		request.SetBasicAuth(state.HTTPUsername, state.HTTPPassword)
		response, err := client.Do(request)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && strings.Contains(string(body), "EverythingShare") {
				return nil
			}
			lastResult = fmt.Sprintf("HTTP %d, %d response bytes", response.StatusCode, len(body))
		} else {
			lastResult = err.Error()
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("bundled Everything did not finish indexing the demo files (%s)", lastResult)
}
