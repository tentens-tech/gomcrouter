package time

import (
	_ "runtime"
	_ "unsafe"
)

//go:linkname NanoTime runtime.nanotime
func NanoTime() int64
