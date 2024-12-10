package hodu

import "syscall"
import "unsafe"
import "golang.org/x/sys/unix"


// utilize the builtin runtime.nanotime()
//go:noescape
//go:linkname nanotime runtime.nanotime
func nanotime() int64

func monotonic_time() uint64 {
	var n int64
	var err error
	var uts unix.Timespec

	n = nanotime() // hopefully it's faster than a system call. say, vdso is utilized.
	if (n >= 0) { return uint64(n) }

	err = unix.ClockGettime(unix.CLOCK_MONOTONIC, &uts)
	if err != nil {
		//var errno syscall.Errno
		var r uintptr
		var sts syscall.Timespec
		r, _, _/*errno*/ = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, unix.CLOCK_MONOTONIC, uintptr(unsafe.Pointer(&sts)), 0)
		if r == ^uintptr(0) { return uint64(n) } // may be negative cast to unsigned. no other fall-back
		return uint64(sts.Nano())
	}

	return uint64(uts.Nano())
}
