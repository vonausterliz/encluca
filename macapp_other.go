//go:build !darwin

package main

func openFromBundle() bool { return false }

func afterReplace(exe string, logf func(string, ...any)) {}
