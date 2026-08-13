//go:build !windows || !bundled_everything

package main

func bundledEverythingEnabled() bool { return false }

func configureBundledEverything(string, standaloneFileConfig) error { return nil }

func ensureBundledEverything(string, standaloneFileConfig) error { return nil }
