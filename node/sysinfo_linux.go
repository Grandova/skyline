//go:build linux

package node

import "syscall"

func readDiskUsage() (total uint64, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil || st.Blocks == 0 {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	free := st.Bavail * bsize
	if total > free {
		used = total - free
	}
	return total, used
}
