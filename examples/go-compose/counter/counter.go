// Package counter is a minimal Redis client (RESP2 over net.Conn) that only
// knows INCR and GET. It exists to give the example something to test.
package counter

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// Client talks RESP to a single connection. Not safe for concurrent use.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
}

// New wraps an open connection.
func New(conn net.Conn) *Client { return &Client{conn: conn, r: bufio.NewReader(conn)} }

// Dial connects to addr over TCP.
func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return New(conn), nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Incr increments key and returns the new value.
func (c *Client) Incr(key string) (int64, error) {
	v, err := c.do("INCR", key)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("INCR: unexpected reply %v", v)
	}
	return n, nil
}

// Get returns the value of key and whether it exists.
func (c *Client) Get(key string) (string, bool, error) {
	v, err := c.do("GET", key)
	if err != nil || v == nil {
		return "", false, err
	}
	s, ok := v.(string)
	if !ok {
		return "", false, fmt.Errorf("GET: unexpected reply %v", v)
	}
	return s, true, nil
}

func (c *Client) do(args ...string) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := c.conn.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	return c.read()
}

// read parses one reply: simple string, error, integer or bulk string.
// ponytail: no arrays, INCR and GET never return them.
func (c *Client) read() (any, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return nil, errors.New("empty reply")
	}
	switch body := line[1:]; line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, errors.New(body)
	case ':':
		return strconv.ParseInt(body, 10, 64)
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n < 0 {
			return nil, err // $-1 is nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	}
	return nil, fmt.Errorf("unsupported reply %q", line)
}
