package counter

import (
	"bufio"
	"net"
	"strconv"
	"testing"
)

// fakeServer answers each request on conn with the next canned reply.
func fakeServer(t *testing.T, conn net.Conn, replies ...string) {
	t.Helper()
	go func() {
		r := bufio.NewReader(conn)
		for _, reply := range replies {
			// Request is "*N" then N pairs of "$len" + value lines.
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			n, _ := strconv.Atoi(line[1 : len(line)-2])
			for range 2 * n {
				if _, err := r.ReadString('\n'); err != nil {
					return
				}
			}
			conn.Write([]byte(reply))
		}
	}()
}

func TestIncrAndGet(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	fakeServer(t, server, ":1\r\n", "$1\r\n1\r\n", "$-1\r\n", "-ERR boom\r\n")
	c := New(client)

	if n, err := c.Incr("k"); err != nil || n != 1 {
		t.Fatalf("Incr = %d, %v; want 1", n, err)
	}
	if v, ok, err := c.Get("k"); err != nil || !ok || v != "1" {
		t.Fatalf("Get = %q, %v, %v; want \"1\", true", v, ok, err)
	}
	if _, ok, err := c.Get("missing"); err != nil || ok {
		t.Fatalf("Get(missing) = %v, %v; want not found", ok, err)
	}
	if _, err := c.Incr("k"); err == nil || err.Error() != "ERR boom" {
		t.Fatalf("Incr error = %v; want ERR boom", err)
	}
}
