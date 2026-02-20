package types

import (
	"github.com/panjf2000/gnet/v2"
)

const (
	PendingRequestStatus int8 = iota
	InflightRequestStatus
	CancelledRequestStatus
	DoneRequestStatus
)

type Request struct {
	Raw *ByteBuf
	Cmd int

	Ts   int64
	Conn gnet.Conn

	Callback func(*ByteBuf, error)
}

type InflightRequest struct {
	Request *Request
	Status  int8
	Ts      int64
}
