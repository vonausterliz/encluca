//go:build !linux && !windows

package main

func ensureTrayAutostart(c *Client, exe string) (bool, error) { return false, nil }

func startTrayChild(exe, dataDir string) error { return nil }
