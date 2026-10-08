//go:build !linux

package ft8ts

// fcntlSetPipeSz is a no-op off Linux.
func fcntlSetPipeSz(fd uintptr, size int) (int, error) { return 0, nil }
