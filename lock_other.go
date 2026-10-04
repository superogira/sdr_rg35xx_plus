//go:build !linux

package main

// lockInstance is a no-op on dev builds.
func lockInstance() bool { return true }
