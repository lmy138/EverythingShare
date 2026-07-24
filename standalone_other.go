//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func readConsoleSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	value, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(value), err
}

func protectConfigFile(configPath string) error {
	return os.Chmod(configPath, 0600)
}

func openBrowserAfterStart(string) {}
