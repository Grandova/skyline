//go:build !linux

package node

func readDiskUsage() (total uint64, used uint64) {
	return 20 * 1024 * 1024 * 1024, 2 * 1024 * 1024 * 1024
}
