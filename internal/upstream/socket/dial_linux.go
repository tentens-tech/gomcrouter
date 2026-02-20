//go:build linux

package socket

import "golang.org/x/sys/unix"

func dialFD(addr string, timeoutMs int) (fd int, err error) {
	sa, family, err := sockAddrFromAddrPort(addr)
	if err != nil {
		return 0, err
	}

	fd, err = unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		return 0, err
	}

	if err = unix.SetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1); err != nil {
		_ = unix.Close(fd)
		return 0, err
	}

	err = unix.Connect(fd, sa)
	if err == nil {
		return fd, nil
	}

	if err == unix.EINPROGRESS || err == unix.EALREADY {
		if err = waitConnect(fd, timeoutMs); err != nil {
			_ = unix.Close(fd)
			return 0, err
		}
		return fd, nil
	}

	_ = unix.Close(fd)
	return 0, err
}
