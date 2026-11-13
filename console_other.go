//go:build !windows

package main

// Fuori da Windows il terminale è sempre collegato: niente da fare.
func attachParentConsole() {}
