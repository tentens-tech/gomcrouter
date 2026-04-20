package ascii

import (
	"github.com/stretchr/testify/assert"
	"github.com/tentens-tech/gomcrouter/internal/proto/errors"
	"testing"
)

func Test_fastIndexCRLF(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want int
	}{
		{
			name: "only_crlf",
			data: []byte("\r\n"),
			want: 0,
		},
		{
			name: "no_crlf",
			data: []byte("abc"),
			want: -1,
		},
		{
			name: "crlf_after_word",
			data: []byte("version\r\n"),
			want: 7,
		},
		{
			name: "empty",
			data: []byte(""),
			want: -1,
		},
		{
			name: "cr_space_lf_not_match",
			data: []byte("\r \n"),
			want: -1,
		},
		{
			name: "many_cr_then_lf",
			data: []byte("\r\r\r\r\n"),
			want: 3,
		},
		{
			name: "leading_lf_then_crlf",
			data: []byte("\n\r\r\r\r\n"),
			want: 4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := fastIndexCRLF(tt.data)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeRequest(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		frame []byte
		cmd   int
		n     int
		err   error
	}{
		{
			name:  "version_ok",
			data:  []byte("version\r\n"),
			frame: []byte("version\r\n"),
			cmd:   VersionCmd,
			n:     9,
			err:   nil,
		},
		{
			name:  "get_ok",
			data:  []byte("get foo\r\n"),
			frame: []byte("get foo\r\n"),
			cmd:   GetCmd,
			n:     9,
			err:   nil,
		},
		{
			name:  "delete_ok",
			data:  []byte("delete mykey\r\n"),
			frame: []byte("delete mykey\r\n"),
			cmd:   DeleteCmd,
			n:     14,
			err:   nil,
		},
		{
			name: "set_missing_bytes_malformed",
			data: []byte("set key 5\r\nabcde\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name:  "cas_ok",
			data:  []byte("cas foo 0 60 5 123\r\nabcde\r\n"),
			frame: []byte("cas foo 0 60 5 123\r\nabcde\r\n"),
			cmd:   CasCmd,
			n:     27,
			err:   nil,
		},
		{
			name: "GET_uppercase_rejected",
			data: []byte("GET foo\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name: "SET_uppercase_rejected",
			data: []byte("SET key 5\r\nabcde\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name: "CAS_uppercase_rejected",
			data: []byte("CAS k 0 0 5 123\r\nabcde\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name: "DELETE_uppercase_rejected",
			data: []byte("DELETE key\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name: "Get_mixedcase_rejected",
			data: []byte("Get foo\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name: "sEt_mixedcase_rejected",
			data: []byte("sEt key 5\r\nabcde\r\n"),
			err:  errors.ErrMalformedRequest,
		},
		{
			name: "caS_mixedcase_rejected",
			data: []byte("caS foo 0 0 5 123\r\nabcde\r\n"),
			err:  errors.ErrMalformedRequest,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			frame, cmd, n, err := DecodeRequest(tc.data)

			assert.Equal(t, tc.frame, frame)
			assert.Equal(t, tc.cmd, cmd)
			assert.Equal(t, tc.n, n)
			assert.Equal(t, tc.err, err)
		})
	}
}

func TestDecodeValueLen(t *testing.T) {
	type tc struct {
		name   string
		header []byte
		pos    int
		want   int
	}
	cases := []tc{
		{
			name:   "no CAS, CRLF",
			header: []byte("VALUE key 0 10\r\n"),
			pos:    4,
			want:   10,
		},
		{
			name:   "with CAS, CRLF",
			header: []byte("VALUE key 0 150 32586016\r\n"),
			pos:    4,
			want:   150,
		},
		{
			name:   "with CAS, LF only",
			header: []byte("VALUE key 0 150 32586016\n"),
			pos:    4,
			want:   150,
		},
		{
			name:   "no CAS, extra spaces",
			header: []byte("VALUE   key   123   42   \r\n"),
			pos:    4,
			want:   42,
		},
		{
			name:   "with CAS, extra spaces",
			header: []byte("VALUE   key   0   7    999999999999\r\n"),
			pos:    4,
			want:   7,
		},
		{
			name:   "zero length",
			header: []byte("VALUE k 0 0\r\n"),
			pos:    4,
			want:   0,
		},
		{
			name:   "long key, no CAS",
			header: []byte("VALUE key-with-dashes_and123 0 1234\r\n"),
			pos:    4,
			want:   1234,
		},
		{
			name:   "tabs treated as spaces (if you normalize elsewhere)",
			header: []byte("VALUE\tkey\t0\t9\r\n"),
			pos:    4,
			want:   -1,
		},
		{
			name:   "multiple VALUE lines: only first header matters here",
			header: []byte("VALUE a 0 3\r\n"),
			pos:    4,
			want:   3,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodeBodyLen(c.header, c.pos)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestClassifyGetResponse(t *testing.T) {
	cases := []struct {
		name string
		resp []byte
		want int8
	}{
		{
			name: "miss",
			resp: []byte("END\r\n"),
			want: GetResponseMiss,
		},
		{
			name: "single hit",
			resp: []byte("VALUE foo 0 3\r\nbar\r\nEND\r\n"),
			want: GetResponseHit,
		},
		{
			name: "multi hit",
			resp: []byte("VALUE foo 0 3\r\nbar\r\nVALUE baz 0 4\r\nquux\r\nEND\r\n"),
			want: GetResponseHit,
		},
		{
			name: "server error ignored",
			resp: []byte("SERVER_ERROR out of memory\r\n"),
			want: GetResponseUnknown,
		},
		{
			name: "client error ignored",
			resp: []byte("CLIENT_ERROR bad command\r\n"),
			want: GetResponseUnknown,
		},
		{
			name: "bare error ignored",
			resp: []byte("ERROR\r\n"),
			want: GetResponseUnknown,
		},
		{
			name: "empty ignored",
			resp: []byte{},
			want: GetResponseUnknown,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyGetResponse(c.resp)
			assert.Equal(t, c.want, got)
		})
	}
}

func Test_DecodeResponse(t *testing.T) {
	type tc struct {
		name  string
		frame []byte
		want  []byte
		err   error
	}
	cases := []tc{
		{
			name:  "version header",
			frame: []byte("VERSION xxx gomcrouter\r\n"),
			want:  []byte("VERSION xxx gomcrouter\r\n"),
			err:   nil,
		},
		{
			name:  "stored header",
			frame: []byte("STORED\r\n"),
			want:  []byte("STORED\r\n"),
			err:   nil,
		},
		{
			name:  "stored header with garbage",
			frame: []byte("STORED\r\nabababababababkqj\r\n\t"),
			want:  []byte("STORED\r\n"),
			err:   nil,
		},
		{
			name:  "end header",
			frame: []byte("END\r\n"),
			want:  []byte("END\r\n"),
			err:   nil,
		},
		{
			name:  "value frame",
			frame: []byte("VALUE foo 0 3\r\nbar\r\nEND\r\n"),
			want:  []byte("VALUE foo 0 3\r\nbar\r\nEND\r\n"),
			err:   nil,
		},
		{
			name:  "value frame with garbage",
			frame: []byte("VALUE foo 0 3\r\nbar\r\nEND\r\n\r\n\bqlkweuflqwkegf n\t"),
			want:  []byte("VALUE foo 0 3\r\nbar\r\nEND\r\n"),
			err:   nil,
		},
		{
			name:  "multi value frame",
			frame: []byte("VALUE foo 0 3\r\nbar\r\nVALUE bar 0 4\r\nbazz\r\nEND\r\n"),
			want:  []byte("VALUE foo 0 3\r\nbar\r\nVALUE bar 0 4\r\nbazz\r\nEND\r\n"),
			err:   nil,
		},
		{
			name:  "multi value frame with garbage",
			frame: []byte("VALUE foo 0 3\r\nbar\r\nVALUE bar 0 4\r\nbazz\r\nEND\r\n\r\r\r\r\r\n\n\n\n\n\t\t\t\tqwjehfdqwfq\\t\t\tqwdefq"),
			want:  []byte("VALUE foo 0 3\r\nbar\r\nVALUE bar 0 4\r\nbazz\r\nEND\r\n"),
			err:   nil,
		},
		{
			name: "stats response",
			frame: []byte(
				"STAT pid 1234\r\n" +
					"STAT uptime 3600\r\n" +
					"STAT time 1700000000\r\n" +
					"STAT version 1.6.21\r\n" +
					"STAT curr_connections 12\r\n" +
					"END\r\n",
			),
			want: []byte(
				"STAT pid 1234\r\n" +
					"STAT uptime 3600\r\n" +
					"STAT time 1700000000\r\n" +
					"STAT version 1.6.21\r\n" +
					"STAT curr_connections 12\r\n" +
					"END\r\n",
			),
			err: nil,
		},
		{
			name: "stats cachedump",
			frame: []byte(
				"ITEM user:1234 [6 b; 1700001200 s]\r\n" +
					"ITEM session:abcd [128 b; 1700001180 s]\r\n" +
					"ITEM config:v1 [42 b; 1700001100 s]\r\n" +
					"ITEM cart:5678 [256 b; 1700001000 s]\r\n" +
					"END\r\n",
			),
			want: []byte(
				"ITEM user:1234 [6 b; 1700001200 s]\r\n" +
					"ITEM session:abcd [128 b; 1700001180 s]\r\n" +
					"ITEM config:v1 [42 b; 1700001100 s]\r\n" +
					"ITEM cart:5678 [256 b; 1700001000 s]\r\n" +
					"END\r\n",
			),
			err: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := DecodeResponse(c.frame)

			assert.Equal(t, c.err, err, "expected error %v, got %v", c.err, err)
			assert.Equal(t, c.want, c.frame[:n], "expected %v, got %v", c.want, c.frame[:n])
		})
	}

}
