package types

type RequestEvent struct {
	Cmd        int
	DurationNs int64
}

type UpstreamRequestEvent struct {
	Cmd        int
	HostId     int
	DurationNs int64
	HitStatus  int8
}
