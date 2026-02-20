//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package socket

import "golang.org/x/sys/unix"

func dialFD(addr string, timeoutMs int) (fd int, err error) {
	sa, family, err := sockAddrFromAddrPort(addr)
	if err != nil {
		return 0, err
	}

	fd, err = unix.Socket(family, unix.SOCK_STREAM, unix.IPPROTO_TCP)
	if err != nil {
		return 0, err
	}

	unix.CloseOnExec(fd)

	if err = unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return 0, err
	}

	if err = unix.SetsockoptInt(fd, unix.IPPROTO_TCP, unix.TCP_NODELAY, 1); err != nil {
		_ = unix.Close(fd)
		return 0, err
	}

	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_NOSIGPIPE, 1)

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
