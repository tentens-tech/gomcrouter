package types

import "sync"

// ConnState keeps the replies of a client connection in the order of its
// requests. Pipelined requests of one connection can be answered by
// different upstream sockets and hosts, so their replies complete out of
// order; a reply that completes early waits in Pending for the replies to the
// requests before it.
type ConnState struct {
	// NextSeq is the sequence number of the next request. It's only used on
	// the event loop of the connection.
	NextSeq uint64
	// Quit is set once the connection is closing: the requests after quit
	// and after a malformed request are dropped. Event loop only.
	Quit bool

	Mu sync.Mutex
	// Written is the sequence number of the next reply to write.
	Written uint64
	Pending map[uint64]Reply
}

// Reply is a reply waiting for its turn to be written.
type Reply struct {
	Resp *ByteBuf
	// Cmd and Ts are reported in the request metrics when Resp is written.
	Cmd int
	Ts  int64
	// Close closes the connection after Resp (if any) is written, without
	// reporting metrics.
	Close bool
}
