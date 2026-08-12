//go:build !windows

package main

func prepareBundledDemo() (func(), error) { return nil, nil }

type demoRuntime struct{}

var bundledDemoRuntime *demoRuntime

func loadBundledDemoConfig(demoRuntime) (config, error) { return config{}, nil }
