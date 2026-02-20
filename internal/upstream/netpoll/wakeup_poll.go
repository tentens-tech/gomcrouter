package netpoll

import (
	"golang.org/x/sys/unix"
)

var WakeupData = []byte{1}

type eventFD struct {
	r int
	w int
}

func newEventFD() (*eventFD, error) {
	var fds [2]int
	if err := unix.Pipe(fds[:]); err != nil {
		return nil, err
	}

	_ = unix.SetNonblock(fds[0], true)
	_ = unix.SetNonblock(fds[1], true)

	return &eventFD{
		r: fds[0],
		w: fds[1],
	}, nil
}

func (e *eventFD) close() {
	_ = unix.Close(e.r)
	_ = unix.Close(e.w)
}

func (e *eventFD) trigger() error {
	for {
		_, err := unix.Write(e.w, WakeupData)
		if err == nil || err == unix.EAGAIN {
			return nil
		}

		if err == unix.EINTR {
			continue
		}

		return err
	}
}

func (e *eventFD) drain() {
	var buf [8]byte
	_, _ = unix.Read(e.r, buf[:])
}
