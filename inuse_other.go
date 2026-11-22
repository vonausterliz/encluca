//go:build !linux && !darwin && !windows

package main

func openByProcesses(work string) ([]inUseFile, error) { return nil, nil }
