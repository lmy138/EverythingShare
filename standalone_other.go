//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func readConsoleSecret(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	value, err := reader.ReadString('\n')
	return strings.TrimSpace(value), err
}

func protectConfigFile(configPath string) error {
	return os.Chmod(configPath, 0600)
}

func openBrowserAfterStart(string) {}
