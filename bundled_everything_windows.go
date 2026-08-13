//go:build windows && bundled_everything

package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	bundledEverythingInstance = "EverythingShare"
	bundledEverythingSHA256   = "f191f756996a14a11e5445fa7103d302efd510cf2fbf920e6c0c8ed51d512e36"
)

// The build script temporarily replaces the tracked placeholder with the
// verified official Everything portable executable.
//
//go:embed third_party/everything/Everything.exe.bin third_party/everything/License.txt third_party/everything/SOURCE.txt
var bundledEverythingFiles embed.FS

func bundledEverythingEnabled() bool { return true }

func configureBundledEverything(configPath string, cfg standaloneFileConfig) error {
	if cfg.BundledEverythingMode == "" {
		return nil
	}
	fmt.Println("\n正在初始化内置 Everything，Windows 可能显示权限确认窗口。")
	if err := startBundledEverything(configPath, cfg, true); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Everything 已启动，正在首次建立文件索引。")
	fmt.Println("首次索引通常需要 1～3 分钟；网页暂时没有文件属于正常现象，请耐心等待后刷新。")
	return nil
}

func ensureBundledEverything(configPath string, cfg standaloneFileConfig) error {
	if cfg.BundledEverythingMode == "" {
		return nil
	}
	return startBundledEverything(configPath, cfg, false)
}

func startBundledEverything(configPath string, cfg standaloneFileConfig, fromWizard bool) error {
	if cfg.BundledEverythingMode != "service" && cfg.BundledEverythingMode != "admin" {
		return errors.New("bundled_everything_mode must be service or admin")
	}
	// The setup wizard starts Everything before returning to the normal startup
	// path. Avoid a second elevation and a second listener when it is already up.
	if probeEverything(cfg) == nil {
		return nil
	}
	parsed, err := url.Parse(cfg.EverythingBaseURL)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || parsed.Scheme != "http" || (host != "127.0.0.1" && host != "localhost") || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("内置 Everything HTTP 地址必须是 http://127.0.0.1:端口")
	}

	root := filepath.Join(filepath.Dir(configPath), "Everything")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	executable := filepath.Join(root, "Everything.exe")
	if err := extractBundledEverything(executable); err != nil {
		return err
	}
	if fromWizard {
		// Reconfiguration must stop the old named client before replacing its
		// credentials or port; otherwise it keeps the old HTTP listener alive.
		exit := exec.Command(executable, "-instance", bundledEverythingInstance, "-exit")
		exit.Dir = root
		_ = exit.Run()
		time.Sleep(750 * time.Millisecond)
	}
	for _, name := range []string{"License.txt", "SOURCE.txt"} {
		raw, readErr := bundledEverythingFiles.ReadFile("third_party/everything/" + name)
		if readErr != nil {
			return readErr
		}
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			return err
		}
	}

	iniPath := filepath.Join(root, "Everything-"+bundledEverythingInstance+".ini")
	if err := updateBundledEverythingINI(iniPath, port, cfg); err != nil {
		return err
	}
	profileMarker := filepath.Join(root, "profile-initialized")
	if _, statErr := os.Stat(profileMarker); errors.Is(statErr, os.ErrNotExist) {
		// Everything portable normally asks how volumes should be indexed on its
		// first run. This documented installation command creates the complete
		// profile without showing that extra dialog. We then re-enable automatic
		// fixed-volume indexing according to the user's service/admin choice.
		initialize := exec.Command(executable,
			"-instance", bundledEverythingInstance,
			"-config", iniPath,
			"-choose-volumes",
		)
		initialize.Dir = root
		if output, initializeErr := initialize.CombinedOutput(); initializeErr != nil {
			return fmt.Errorf("初始化 Everything 索引配置失败: %w (%s)", initializeErr, strings.TrimSpace(string(output)))
		}
		if err := updateBundledEverythingINI(iniPath, port, cfg); err != nil {
			return err
		}
		if err := os.WriteFile(profileMarker, []byte("Everything 1.4 portable profile\r\n"), 0600); err != nil {
			return err
		}
	}

	if cfg.BundledEverythingMode == "service" {
		if !bundledEverythingServiceInstalled() {
			fmt.Println("请在 Windows 权限窗口中允许安装 Everything Service。")
			install := exec.Command(executable,
				"-install-service",
				"-instance", bundledEverythingInstance,
				"-install-service-pipe-name", "EverythingShare Service",
			)
			install.Dir = root
			if output, installErr := install.CombinedOutput(); installErr != nil {
				return fmt.Errorf("安装 Everything Service 失败或被取消: %w (%s)", installErr, strings.TrimSpace(string(output)))
			}
			time.Sleep(1500 * time.Millisecond)
		}
		_ = exec.Command("sc.exe", "start", "Everything (EverythingShare)").Run()
	}

	// Open the Everything search window directly after setup. Using -startup
	// would leave it hidden in the tray and make users open it manually.
	args := []string{"-instance", bundledEverythingInstance}
	if cfg.BundledEverythingMode == "admin" {
		if fromWizard {
			fmt.Println("请在 Windows 权限窗口中允许 Everything 以管理员身份运行。")
		}
		if err := startBundledEverythingElevated(executable, root, args); err != nil {
			return err
		}
	} else {
		command := exec.Command(executable, args...)
		command.Dir = root
		if err := command.Start(); err != nil {
			return fmt.Errorf("启动内置 Everything: %w", err)
		}
	}
	if err := waitForBundledEverything(cfg, 60*time.Second); err != nil {
		return err
	}
	return nil
}

func bundledEverythingServiceInstalled() bool {
	return exec.Command("sc.exe", "query", "Everything (EverythingShare)").Run() == nil
}

func startBundledEverythingElevated(executable, directory string, args []string) error {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	argumentLine := strings.Join(args, " ")
	script := "$ErrorActionPreference='Stop'; Start-Process -FilePath " + quote(executable) +
		" -ArgumentList " + quote(argumentLine) + " -WorkingDirectory " + quote(directory) + " -Verb RunAs"
	command := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("管理员权限被取消或 Everything 启动失败: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func waitForBundledEverything(cfg standaloneFileConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := probeEverything(cfg); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("内置 Everything HTTP Server 未能启动: %w", lastErr)
}

func extractBundledEverything(path string) error {
	payload, err := bundledEverythingFiles.ReadFile("third_party/everything/Everything.exe.bin")
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if len(payload) < 2 || payload[0] != 'M' || payload[1] != 'Z' || hex.EncodeToString(digest[:]) != bundledEverythingSHA256 {
		return errors.New("安装包未包含校验通过的官方 Everything.exe")
	}
	if existing, readErr := os.ReadFile(path); readErr == nil {
		existingDigest := sha256.Sum256(existing)
		if len(existing) >= 2 && existing[0] == 'M' && existing[1] == 'Z' && hex.EncodeToString(existingDigest[:]) == bundledEverythingSHA256 {
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

func updateBundledEverythingINI(path, port string, cfg standaloneFileConfig) error {
	values := map[string]string{
		"app_data": "0",
		// Administrator mode is elevated explicitly with ShellExecute/runas.
		// Keeping this disabled prevents Everything from prompting a second time.
		"run_as_admin":                    "0",
		"allow_multiple_instances":        "1",
		"run_in_background":               "0",
		"instance_name":                   bundledEverythingInstance,
		"allow_http_server":               "1",
		"http_server_enabled":             "1",
		"http_server_bindings":            "127.0.0.1",
		"http_server_port":                port,
		"http_server_username":            cfg.EverythingUsername,
		"http_server_password":            cfg.EverythingPassword,
		"http_server_allow_file_download": "1",
		"http_server_logging_enabled":     "0",
		"check_for_updates_on_startup":    "0",
		"auto_include_fixed_volumes":      "1",
		"auto_include_removable_volumes":  "0",
		"show_tray_icon":                  "1",
	}
	if cfg.BundledEverythingMode == "service" {
		values["service_pipe_name"] = `\\.\PIPE\EverythingShare Service`
	} else {
		values["service_pipe_name"] = ""
	}
	raw, err := os.ReadFile(path)
	newFile := errors.Is(err, os.ErrNotExist)
	if err != nil && !newFile {
		return err
	}
	lines := []string{"[Everything]"}
	if !newFile {
		lines = strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	}
	seen := make(map[string]bool, len(values))
	for index, line := range lines {
		key, _, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found {
			continue
		}
		if value, ok := values[key]; ok {
			lines[index] = key + "=" + value
			seen[key] = true
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, key+"="+values[key])
	}
	if newFile {
		lines = append(lines, "show_tray_icon=1")
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0600)
}
