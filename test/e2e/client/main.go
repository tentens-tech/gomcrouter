// Command e2e-client is a small memcached text protocol client used by the
// end-to-end tests.
//
//	e2e-client [-addr host:port] raw 'set k 0 0 1\r\nv\r\n' 'get k\r\n'
//	    sends every argument (\r, \n and \t are unescaped) and prints the
//	    responses. A response ends when the server is idle for -idle. With
//	    -closed it fails unless the server closes the connection.
//	e2e-client [-addr host:port] load -conns 16 -ops 1000 -size 100 -pipeline 8
//	    every connection writes unique keys, reads them back in batches of
//	    -pipeline requests and checks the values. Exits 1 on any mismatch.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "memcached address")
	timeout := flag.Duration("timeout", 5*time.Second, "dial and read timeout")
	idle := flag.Duration("idle", 300*time.Millisecond, "raw: a response ends after this idle time")
	closed := flag.Bool("closed", false, "raw: fail unless the server closes the connection")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: e2e-client [flags] raw <request>... | load [flags]")
		os.Exit(2)
	}

	var err error
	switch flag.Arg(0) {
	case "raw":
		err = raw(*addr, *timeout, *idle, *closed, flag.Args()[1:])
	case "load":
		err = load(*addr, *timeout, flag.Args()[1:])
	default:
		err = fmt.Errorf("unknown mode %q", flag.Arg(0))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e-client:", err)
		os.Exit(1)
	}
}

func unescape(s string) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t").Replace(s)
}

func raw(addr string, timeout, idle time.Duration, closed bool, reqs []string) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 64*1024)
	eof := false
	for _, req := range reqs {
		if _, err = conn.Write([]byte(unescape(req))); err != nil {
			return err
		}
		// the first byte may take up to timeout, the rest of the response
		// ends when the connection is idle
		deadline := timeout
		for {
			_ = conn.SetReadDeadline(time.Now().Add(deadline))
			n, rerr := conn.Read(buf)
			_, _ = os.Stdout.Write(buf[:n])
			if rerr != nil {
				var nerr net.Error
				if errors.Is(rerr, io.EOF) {
					eof = true
					break
				}
				if errors.As(rerr, &nerr) && nerr.Timeout() {
					break
				}
				return rerr
			}
			deadline = idle
		}
	}
	if closed && !eof {
		return errors.New("the server didn't close the connection")
	}
	return nil
}

func load(addr string, timeout time.Duration, args []string) error {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	conns := fs.Int("conns", 8, "connections")
	ops := fs.Int("ops", 500, "keys per connection")
	size := fs.Int("size", 100, "value size in bytes")
	pipeline := fs.Int("pipeline", 1, "requests sent before reading the responses")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pipeline < 1 {
		*pipeline = 1
	}

	prefix := make([]byte, 4)
	_, _ = rand.Read(prefix)
	run := hex.EncodeToString(prefix)

	var failures atomic.Int64
	var done atomic.Int64
	var mu sync.Mutex
	var firstErr error
	fail := func(err error) {
		failures.Add(1)
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	start := time.Now()
	var wg sync.WaitGroup
	for c := 0; c < *conns; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			if err := loadConn(addr, timeout, run, c, *ops, *size, *pipeline, &done); err != nil {
				fail(fmt.Errorf("conn %d: %w", c, err))
			}
		}(c)
	}
	wg.Wait()

	elapsed := time.Since(start)
	total := done.Load()
	fmt.Printf("ops=%d failures=%d elapsed=%s rate=%.0f/s\n",
		total, failures.Load(), elapsed.Round(time.Millisecond), float64(total)/elapsed.Seconds())
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func value(run string, c, i, size int) []byte {
	seed := fmt.Sprintf("%s-%d-%d|", run, c, i)
	v := bytes.Repeat([]byte(seed), size/len(seed)+1)
	return v[:size]
}

func loadConn(addr string, timeout time.Duration, run string, c, ops, size, pipeline int, done *atomic.Int64) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReaderSize(conn, 64*1024)
	w := bufio.NewWriterSize(conn, 64*1024)

	key := func(i int) string { return fmt.Sprintf("e2e:%s:%d:%d", run, c, i) }

	for base := 0; base < ops; base += pipeline {
		n := min(pipeline, ops-base)

		_ = conn.SetDeadline(time.Now().Add(timeout))
		for i := base; i < base+n; i++ {
			v := value(run, c, i, size)
			_, _ = fmt.Fprintf(w, "set %s 0 0 %d\r\n", key(i), len(v))
			_, _ = w.Write(v)
			_, _ = w.WriteString("\r\n")
		}
		if err = w.Flush(); err != nil {
			return err
		}
		for i := base; i < base+n; i++ {
			line, rerr := r.ReadString('\n')
			if rerr != nil {
				return fmt.Errorf("set %s: %w", key(i), rerr)
			}
			if line != "STORED\r\n" {
				return fmt.Errorf("set %s: %q", key(i), line)
			}
			done.Add(1)
		}

		for i := base; i < base+n; i++ {
			_, _ = fmt.Fprintf(w, "get %s\r\n", key(i))
		}
		if err = w.Flush(); err != nil {
			return err
		}
		for i := base; i < base+n; i++ {
			got, rerr := readValue(r, key(i))
			if rerr != nil {
				return fmt.Errorf("get %s: %w", key(i), rerr)
			}
			if want := value(run, c, i, size); !bytes.Equal(got, want) {
				return fmt.Errorf("get %s: value mismatch: got %q, want %q", key(i), head(got), head(want))
			}
			done.Add(1)
		}
	}
	return nil
}

func head(b []byte) []byte {
	if len(b) > 32 {
		return b[:32]
	}
	return b
}

// readValue reads a single-key get response: VALUE <key> <flags> <bytes>\r\n<data>\r\nEND\r\n
func readValue(r *bufio.Reader, key string) ([]byte, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "VALUE ") {
		return nil, fmt.Errorf("unexpected response %q", line)
	}
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return nil, fmt.Errorf("malformed VALUE line %q", line)
	}
	if fields[1] != key {
		return nil, fmt.Errorf("key mismatch: got %q", fields[1])
	}
	n, err := strconv.Atoi(fields[3])
	if err != nil {
		return nil, fmt.Errorf("malformed VALUE line %q", line)
	}
	data := make([]byte, n+2)
	if _, err = io.ReadFull(r, data); err != nil {
		return nil, err
	}
	end, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if end != "END\r\n" {
		return nil, fmt.Errorf("expected END, got %q", end)
	}
	return data[:n], nil
}
