//go:build linux
package hodu

import "syscall"
import "unsafe"

//#include <time.h>
//import "C"

// C.CLOCK_MONOTONIC is more accurate when compiled with CGO.
// I want to avoid using it. so assume it is 1 on linux
//const CLOCK_MONOTONIC uintptr = C.CLOCK_MONOTONIC
const CLOCK_MONOTONIC uintptr = 1

// utilize the builtin runtime.nanotime()
//go:noescape
//go:linkname nanotime runtime.nanotime
func nanotime() int64

func monotonic_time() uint64 {
	var ts syscall.Timespec
	var errno syscall.Errno
	_, _, errno = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, CLOCK_MONOTONIC, uintptr(unsafe.Pointer(&ts)), 0)
	if errno != 0 { return uint64(nanotime()) }
	return uint64(ts.Nano())
}
