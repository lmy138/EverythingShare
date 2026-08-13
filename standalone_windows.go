//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func readConsoleSecret(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	handle := windows.Handle(os.Stdin.Fd())
	var originalMode uint32
	if err := windows.GetConsoleMode(handle, &originalMode); err != nil {
		value, readErr := reader.ReadString('\n')
		return strings.TrimSpace(value), readErr
	}
	if err := windows.SetConsoleMode(handle, originalMode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", err
	}
	defer windows.SetConsoleMode(handle, originalMode)
	value, err := reader.ReadString('\n')
	fmt.Println()
	return strings.TrimSpace(value), err
}

func protectConfigFile(configPath string) error {
	current, err := user.Current()
	if err != nil {
		return err
	}
	account := current.Username
	return exec.Command(
		"icacls",
		configPath,
		"/inheritance:r",
		"/grant:r",
		account+":(F)",
		"*S-1-5-18:(F)",
		"*S-1-5-32-544:(F)",
	).Run()
}

func openBrowserAfterStart(target string) {
	time.Sleep(900 * time.Millisecond)
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
}
