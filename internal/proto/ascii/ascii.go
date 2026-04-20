package ascii

import (
	"bytes"
	"github.com/tentens-tech/gomcrouter/internal/proto/errors"
	"github.com/tentens-tech/gomcrouter/internal/types"
	"github.com/tentens-tech/gomcrouter/version"
)

var (
	ValueMarker = []byte("VALUE")
	StatMarker  = []byte("STAT")
	ItemMarker  = []byte("ITEM")
	ErrMarker   = []byte("ERROR")
)

var (
	EndResponse       = []byte("END\r\n")
	NotStoredResponse = []byte("NOT_STORED\r\n")
	ExistsResponse    = []byte("EXISTS\r\n")
	NotFoundResponse  = []byte("NOT_FOUND\r\n")
)

var (
	HandlerMismatchBuf = &types.ByteBuf{
		B: []byte("SERVER_ERROR gomcrouter: request does not match policy handler\r\n"),
	}

	NoHealthyUpstreamErrorResponseBuf = &types.ByteBuf{
		B: []byte("SERVER_ERROR gomcrouter: no healthy upstream\r\n"),
	}

	ErrMalformedRequestErrorResponseBuf = &types.ByteBuf{
		B: []byte("CLIENT_ERROR gomcrouter: malformed request\r\n"),
	}

	ErrProxyErrorResponseBuf = &types.ByteBuf{
		B: []byte("SERVER_ERROR gomcrouter: proxy error\r\n"),
	}

	VersionBuf = &types.ByteBuf{
		B: []byte("VERSION " + version.Version + " gomcrouter\r\n"),
	}
)

var Commands = [][]byte{
	// two-CRLF
	[]byte("set"),
	[]byte("add"),
	[]byte("replace"),
	[]byte("append"),
	[]byte("prepend"),
	[]byte("cas"),

	// one-CRLF
	[]byte("get"),
	[]byte("gets"),
	[]byte("delete"),
	[]byte("incr"),
	[]byte("decr"),
	[]byte("touch"),
	[]byte("stats"),
	[]byte("version"),
	[]byte("verbosity"),
	[]byte("flush_all"),
	[]byte("quit"),
}

// Commands enum. should match to order in Commands array.
// Core idea is to be able to get cmd with O(1), bypassing maps.
// Example: Commands[SetCmd]
const (
	SetCmd = iota
	AddCmd
	ReplaceCmd
	AppendCmd
	PrependCmd
	CasCmd
	GetCmd
	GetsCmd
	DeleteCmd
	IncrCmd
	DecrCmd
	TouchCmd
	StatsCmd
	VersionCmd
	VerbosityCmd
	FlushAllCmd
	QuitCmd
)

var dataCommands = [][]byte{
	[]byte("set"),
	[]byte("add"),
	[]byte("replace"),
	[]byte("append"),
	[]byte("prepend"),
	[]byte("cas"),
}

func getCmd(cmd []byte, group [][]byte) (bool, int) {
	for i, groupCmd := range group {
		if len(groupCmd) == len(cmd) && bytes.Equal(groupCmd, cmd) {
			return true, i
		}
	}
	return false, 0
}

func fastIndexCRLF(buf []byte) int {
	for i := 0; i < len(buf)-1; i++ {
		if buf[i] == '\r' && buf[i+1] == '\n' {
			return i
		}
	}
	return -1
}

func decodeBodyLen(header []byte, pos int) int {
	parts := 0

	wasPart := false
	var x uint
	for i := 0; i < len(header); i++ {
		if header[i] == ' ' {
			if !wasPart {
				continue
			}
			parts++
			wasPart = false
			if parts == pos {
				return int(x)
			}
			x = 0
			continue
		}

		wasPart = true
		d := header[i] - '0'
		if d <= 9 {
			if parts < 2 {
				continue
			}

			x *= 10
			x += uint(d)

			if i == len(header)-1 && parts+1 == pos {
				return int(x)
			}
			continue
		}
		if (header[i] == '\r' || header[i] == '\n') && parts+1 == pos {
			return int(x)
		}

	}
	return -1
}

func DecodeRequest(buf []byte) ([]byte, int, int, error) {
	idx := fastIndexCRLF(buf)
	if idx == -1 {
		return nil, 0, 0, nil
	}

	header := buf[:idx]

	iSpace := bytes.IndexByte(header, ' ')
	var op []byte
	if iSpace == -1 {
		op = header
		ok, cmd := getCmd(op, Commands)
		if !ok {
			return nil, 0, 0, errors.ErrMalformedRequest
		}
		return buf[:idx+2], cmd, idx + 2, nil
	}

	op = header[:iSpace]
	ok, cmd := getCmd(op, Commands)
	if !ok {
		return nil, 0, 0, errors.ErrMalformedRequest
	}

	// non-data commands
	if isDataCmd, _ := getCmd(op, dataCommands); !isDataCmd {
		return buf[:idx+2], cmd, idx + 2, nil
	}

	bodyLen := decodeBodyLen(header, 5)
	if bodyLen < 0 {
		return nil, 0, 0, errors.ErrMalformedRequest
	}

	total := idx + bodyLen + 4
	if total > len(buf) {
		return nil, 0, 0, nil
	}

	return buf[:total], cmd, total, nil
}

const (
	GetResponseUnknown int8 = iota
	GetResponseHit
	GetResponseMiss
)

// ClassifyGetResponse classifies a decoded get/gets upstream response.
// Returns GetResponseHit if the response carries at least one VALUE line,
// GetResponseMiss for a bare END terminator, and GetResponseUnknown for
// anything else (protocol errors, SERVER_ERROR/CLIENT_ERROR, etc.) so
// those cases are not counted as either hit or miss.
func ClassifyGetResponse(resp []byte) int8 {
	if bytes.HasPrefix(resp, ValueMarker) {
		return GetResponseHit
	}
	if bytes.Equal(resp, EndResponse) {
		return GetResponseMiss
	}
	return GetResponseUnknown
}

func UpstreamRequestFailed(resp []byte) bool {
	idx := fastIndexCRLF(resp)
	if idx == -1 {
		return true
	}

	opHeader := resp[:idx]
	if bytes.Contains(opHeader, ErrMarker) {
		return true
	}

	if bytes.Equal(resp, EndResponse) ||
		bytes.Equal(resp, NotStoredResponse) ||
		bytes.Equal(resp, ExistsResponse) ||
		bytes.Equal(resp, NotFoundResponse) {
		return true
	}
	return false
}

func DecodeResponse(resp []byte) (int, error) {
	iCRLF := fastIndexCRLF(resp)
	if iCRLF == -1 {
		return 0, nil
	}

	offset := iCRLF + 2

	header := resp[:iCRLF+2]
	if !bytes.HasPrefix(header, ValueMarker) {
		if bytes.HasPrefix(header, StatMarker) || bytes.HasPrefix(header, ItemMarker) {
			n, err := decodeStats(resp[offset:])
			if err != nil {
				return 0, err
			}

			offset += n
		}

		return offset, nil
	}

	for {
		bodyLen := decodeBodyLen(header, 4)
		offset += bodyLen + 2
		if offset > len(resp) {
			return 0, nil
		}

		nextICRLF := fastIndexCRLF(resp[offset:])
		if nextICRLF == -1 {
			return 0, nil
		}

		header = resp[offset : offset+nextICRLF+2]
		offset += nextICRLF + 2
		if bytes.Equal(header, EndResponse) {
			break
		}

		if !bytes.HasPrefix(header, ValueMarker) {
			return 0, errors.ErrMalformedResponse
		}
	}

	return offset, nil
}

func decodeStats(b []byte) (int, error) {
	offset := 0

	for {
		iCRLF := fastIndexCRLF(b[offset:])
		if iCRLF == -1 {
			return 0, errors.ErrMalformedResponse
		}

		if bytes.Equal(b[offset:], EndResponse) {
			offset += iCRLF + 2
			break
		}

		offset += iCRLF + 2
	}

	return offset, nil
}
