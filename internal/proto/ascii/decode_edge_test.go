package ascii

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/internal/proto/errors"
)

func TestCommandsEnumMatchesTable(t *testing.T) {
	enum := map[int]string{
		SetCmd: "set", AddCmd: "add", ReplaceCmd: "replace", AppendCmd: "append",
		PrependCmd: "prepend", CasCmd: "cas", GetCmd: "get", GetsCmd: "gets",
		DeleteCmd: "delete", IncrCmd: "incr", DecrCmd: "decr", TouchCmd: "touch",
		StatsCmd: "stats", VersionCmd: "version", VerbosityCmd: "verbosity",
		FlushAllCmd: "flush_all", QuitCmd: "quit",
	}
	require.Len(t, Commands, len(enum))
	for idx, name := range enum {
		assert.Equal(t, name, string(Commands[idx]), "Commands[%d]", idx)
	}
	// data commands must be a prefix of Commands so indexes agree
	for i, c := range dataCommands {
		assert.Equal(t, Commands[i], c)
	}
}

func TestDecodeRequest_AllCommands(t *testing.T) {
	for idx, c := range Commands {
		name := string(c)
		t.Run(name, func(t *testing.T) {
			var req string
			switch idx {
			case SetCmd, AddCmd, ReplaceCmd, AppendCmd, PrependCmd:
				req = name + " k 0 0 3\r\nabc\r\n"
			case CasCmd:
				req = name + " k 0 0 3 77\r\nabc\r\n"
			default:
				req = name + " arg\r\n"
			}
			buf := []byte(req + "get next\r\n") // pipelined follow-up must not be consumed
			frame, cmd, n, err := DecodeRequest(buf)
			require.NoError(t, err)
			assert.Equal(t, idx, cmd)
			assert.Equal(t, len(req), n)
			assert.Equal(t, req, string(frame))
		})
	}
}

func TestDecodeRequest_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		frame string
		cmd   int
		n     int
		err   error
	}{
		{name: "empty", data: ""},
		{name: "no_crlf_yet", data: "get fo"},
		{name: "lone_cr", data: "get foo\r"},
		{name: "unknown_single_word", data: "bogus\r\n", err: errors.ErrMalformedRequest},
		{name: "empty_line", data: "\r\n", err: errors.ErrMalformedRequest},
		{name: "leading_space", data: " get foo\r\n", err: errors.ErrMalformedRequest},
		{name: "unknown_with_args", data: "bogus a b\r\n", err: errors.ErrMalformedRequest},
		{name: "prefix_of_command", data: "ge foo\r\n", err: errors.ErrMalformedRequest},
		{name: "command_with_suffix", data: "getx foo\r\n", err: errors.ErrMalformedRequest},
		{name: "quit_bare", data: "quit\r\n", frame: "quit\r\n", cmd: QuitCmd, n: 6},
		{name: "get_bare", data: "get\r\n", frame: "get\r\n", cmd: GetCmd, n: 5},
		{name: "multi_get", data: "get a b c\r\n", frame: "get a b c\r\n", cmd: GetCmd, n: 11},
		{name: "set_body_incomplete", data: "set k 0 0 5\r\nab"},
		{name: "set_body_missing_trailing_crlf", data: "set k 0 0 5\r\nabcde"},
		{
			name:  "set_noreply",
			data:  "set k 0 0 5 noreply\r\nabcde\r\n",
			frame: "set k 0 0 5 noreply\r\nabcde\r\n",
			cmd:   SetCmd,
			n:     28,
		},
		{
			name:  "set_zero_length",
			data:  "set k 0 0 0\r\n\r\n",
			frame: "set k 0 0 0\r\n\r\n",
			cmd:   SetCmd,
			n:     15,
		},
		{
			name:  "set_digits_in_key_and_flags",
			data:  "set 123 42 3600 2\r\nhi\r\n",
			frame: "set 123 42 3600 2\r\nhi\r\n",
			cmd:   SetCmd,
			n:     23,
		},
		{
			name:  "set_body_contains_crlf",
			data:  "set k 0 0 4\r\n\r\n\r\n\r\n",
			frame: "set k 0 0 4\r\n\r\n\r\n\r\n",
			cmd:   SetCmd,
			n:     19,
		},
		{name: "set_non_numeric_bytes", data: "set k 0 0 abc\r\nabc\r\n", err: errors.ErrMalformedRequest},
		{name: "set_missing_fields", data: "set k\r\n", err: errors.ErrMalformedRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frame, cmd, n, err := DecodeRequest([]byte(tc.data))

			require.ErrorIs(t, err, tc.err)
			if tc.frame == "" {
				assert.Nil(t, frame)
			} else {
				assert.Equal(t, tc.frame, string(frame))
			}
			assert.Equal(t, tc.cmd, cmd)
			assert.Equal(t, tc.n, n)
		})
	}
}

// TestDecodeRequest_HugeBodyLenDoesNotPanic covers declared body lengths
// close to MaxInt: idx+bodyLen+4 used to overflow to a negative total and
// make buf[:total] panic, letting a single client request crash the router.
func TestDecodeRequest_HugeBodyLenDoesNotPanic(t *testing.T) {
	for _, l := range []string{
		strconv.FormatInt(1<<63-1, 10),
		strconv.FormatInt(1<<63-3, 10),
		strconv.FormatInt(1<<62, 10),
		"99999999999999999999999", // wraps around uint64 while being parsed
	} {
		t.Run(l, func(t *testing.T) {
			data := []byte("set k 0 0 " + l + "\r\nabc\r\n")
			require.NotPanics(t, func() {
				frame, _, n, err := DecodeRequest(data)
				if err == nil {
					// treated as "need more data"
					assert.Nil(t, frame)
					assert.Zero(t, n)
				} else {
					require.ErrorIs(t, err, errors.ErrMalformedRequest)
				}
			})
		})
	}
}

func TestDecodeRequest_Pipelined(t *testing.T) {
	buf := []byte("get a\r\nset k 0 0 3\r\nabc\r\ndelete k\r\nversion\r\nget b")

	var frames []string
	var cmds []int
	for {
		frame, cmd, n, err := DecodeRequest(buf)
		require.NoError(t, err)
		if n == 0 {
			break
		}
		frames = append(frames, string(frame))
		cmds = append(cmds, cmd)
		buf = buf[n:]
	}

	assert.Equal(t, []string{"get a\r\n", "set k 0 0 3\r\nabc\r\n", "delete k\r\n", "version\r\n"}, frames)
	assert.Equal(t, []int{GetCmd, SetCmd, DeleteCmd, VersionCmd}, cmds)
	assert.Equal(t, "get b", string(buf), "incomplete tail must be left for the next read")
}

func TestDecodeBodyLen_RequestHeaders(t *testing.T) {
	tests := []struct {
		header string
		want   int
	}{
		{"set k 0 0 5", 5},
		{"set k 0 0 5 noreply", 5},
		{"set k 0 0 5 ", 5},
		{"set k 1 -1 5", 5},
		{"cas k 0 0 12 999", 12},
		{"set k 0 0", -1},
		{"set k 0 0 5x", -1},
		{"", -1},
	}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			assert.Equal(t, tt.want, decodeBodyLen([]byte(tt.header), 5))
		})
	}
}

func TestUpstreamRequestFailed(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want bool
	}{
		{name: "empty", resp: "", want: true},
		{name: "incomplete", resp: "STORED", want: true},
		{name: "error", resp: "ERROR\r\n", want: true},
		{name: "server_error", resp: "SERVER_ERROR out of memory\r\n", want: true},
		{name: "client_error", resp: "CLIENT_ERROR bad data chunk\r\n", want: true},
		{name: "miss", resp: "END\r\n", want: true},
		{name: "not_stored", resp: "NOT_STORED\r\n", want: true},
		{name: "exists", resp: "EXISTS\r\n", want: true},
		{name: "not_found", resp: "NOT_FOUND\r\n", want: true},
		{name: "stored", resp: "STORED\r\n", want: false},
		{name: "deleted", resp: "DELETED\r\n", want: false},
		{name: "touched", resp: "TOUCHED\r\n", want: false},
		{name: "incr_value", resp: "42\r\n", want: false},
		{name: "hit", resp: "VALUE k 0 3\r\nbar\r\nEND\r\n", want: false},
		{name: "version", resp: "VERSION 1.6.21\r\n", want: false},
		// ERROR inside the value body must not be treated as an error
		{name: "hit_with_error_in_body", resp: "VALUE k 0 5\r\nERROR\r\nEND\r\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, UpstreamRequestFailed([]byte(tt.resp)))
		})
	}
}

func TestDecodeResponse_Incomplete(t *testing.T) {
	full := "VALUE foo 0 3\r\nbar\r\nVALUE baz 0 4\r\nquux\r\nEND\r\n"
	// every strict prefix of a complete response must be reported as
	// "need more data" rather than as a frame or an error
	for i := 0; i < len(full); i++ {
		n, err := DecodeResponse([]byte(full[:i]))
		require.NoError(t, err, "prefix %q", full[:i])
		require.Zero(t, n, "prefix %q", full[:i])
	}
	n, err := DecodeResponse([]byte(full))
	require.NoError(t, err)
	assert.Equal(t, len(full), n)
}

func TestDecodeResponse_Malformed(t *testing.T) {
	tests := []struct {
		name string
		resp string
	}{
		{name: "garbage_after_value", resp: "VALUE foo 0 3\r\nbar\r\nGARBAGE\r\n"},
		{name: "value_header_without_length", resp: "VALUE foo 0\r\nbar\r\nEND\r\n"},
		{name: "value_header_non_numeric_length", resp: "VALUE foo 0 x\r\nbar\r\nEND\r\n"},
		// a missing length used to be read as -1 and silently shift the
		// offset by one byte, accepting this as a valid frame
		{name: "value_header_without_length_shifted", resp: "VALUE foo 0\r\nXEND\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := DecodeResponse([]byte(tt.resp))
			require.ErrorIs(t, err, errors.ErrMalformedResponse)
			assert.Zero(t, n)
		})
	}
}

// TestDecodeResponse_HugeValueLenDoesNotPanic: a VALUE header declaring a
// length near MaxInt used to overflow the offset and panic on resp[offset:].
func TestDecodeResponse_HugeValueLenDoesNotPanic(t *testing.T) {
	for _, l := range []string{strconv.FormatInt(1<<63-1, 10), strconv.FormatInt(1<<63-3, 10)} {
		t.Run(l, func(t *testing.T) {
			resp := []byte("VALUE k 0 " + l + "\r\nabc\r\nEND\r\n")
			require.NotPanics(t, func() {
				n, err := DecodeResponse(resp)
				require.NoError(t, err)
				assert.Zero(t, n)
			})
		})
	}
}

func TestDecodeResponse_Pipelined(t *testing.T) {
	responses := []string{
		"STORED\r\n",
		"VALUE foo 0 3\r\nbar\r\nEND\r\n",
		"STAT pid 1\r\nSTAT uptime 2\r\nEND\r\n",
		"END\r\n",
		"ITEM k [1 b; 0 s]\r\nEND\r\n",
		"VALUE a 0 1 5\r\nx\r\nVALUE b 0 2 6\r\nyy\r\nEND\r\n",
		"DELETED\r\n",
	}
	buf := []byte(strings.Join(responses, ""))

	var got []string
	offset := 0
	for {
		n, err := DecodeResponse(buf[offset:])
		require.NoError(t, err, "at offset %d", offset)
		if n == 0 {
			break
		}
		got = append(got, string(buf[offset:offset+n]))
		offset += n
	}
	assert.Equal(t, responses, got)
	assert.Equal(t, len(buf), offset)
}

// TestDecodeResponse_StatsIncomplete: a stats reply split across TCP reads
// must be reported as "need more data" (0, nil). It used to be reported as
// ErrMalformedResponse, which makes the upstream poll group drop the socket.
func TestDecodeResponse_StatsIncomplete(t *testing.T) {
	full := "STAT pid 1\r\nSTAT uptime 2\r\nEND\r\n"
	for i := len("STAT pid 1\r\n"); i < len(full); i++ {
		n, err := DecodeResponse([]byte(full[:i]))
		require.NoError(t, err, "prefix %q", full[:i])
		require.Zero(t, n, "prefix %q", full[:i])
	}
}

func TestDecodeResponse_SingleLineReplies(t *testing.T) {
	for _, r := range []string{
		"STORED\r\n", "NOT_STORED\r\n", "EXISTS\r\n", "NOT_FOUND\r\n", "DELETED\r\n",
		"TOUCHED\r\n", "OK\r\n", "42\r\n", "ERROR\r\n", "SERVER_ERROR oom\r\n",
		"CLIENT_ERROR bad\r\n", "END\r\n",
	} {
		t.Run(strings.TrimSpace(r), func(t *testing.T) {
			n, err := DecodeResponse([]byte(r + "trailing"))
			require.NoError(t, err)
			assert.Equal(t, len(r), n)
		})
	}
}

func TestStaticResponseBuffers(t *testing.T) {
	for name, b := range map[string][]byte{
		"HandlerMismatchBuf":                  HandlerMismatchBuf.B,
		"NoHealthyUpstreamErrorResponseBuf":   NoHealthyUpstreamErrorResponseBuf.B,
		"ErrMalformedRequestErrorResponseBuf": ErrMalformedRequestErrorResponseBuf.B,
		"ErrProxyErrorResponseBuf":            ErrProxyErrorResponseBuf.B,
		"VersionBuf":                          VersionBuf.B,
	} {
		t.Run(name, func(t *testing.T) {
			// each static reply is exactly one well-formed line
			n, err := DecodeResponse(b)
			require.NoError(t, err)
			assert.Equal(t, len(b), n)
			assert.Equal(t, len(b)-2, fastIndexCRLF(b))
		})
	}
	assert.True(t, strings.HasPrefix(string(VersionBuf.B), "VERSION "))
}

func BenchmarkDecodeRequest(b *testing.B) {
	cases := map[string][]byte{
		"get": []byte("get some:reasonably:long:key:12345\r\n"),
		"set": []byte("set some:key 0 3600 64\r\n" + strings.Repeat("x", 64) + "\r\n"),
	}
	for name, buf := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _, _, _ = DecodeRequest(buf)
			}
		})
	}
}

func BenchmarkDecodeResponse(b *testing.B) {
	cases := map[string][]byte{
		"stored": []byte("STORED\r\n"),
		"hit":    []byte("VALUE some:key 0 64\r\n" + strings.Repeat("x", 64) + "\r\nEND\r\n"),
		"miss":   []byte("END\r\n"),
	}
	for name, buf := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _ = DecodeResponse(buf)
			}
		})
	}
}
