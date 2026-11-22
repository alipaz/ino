package postgres

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsMalformedURLWithoutLeakingCredentials(t *testing.T) {
	pool, err := Open(context.Background(), "postgres://ino:secret@localhost:invalid/ino")
	if pool != nil || err == nil {
		t.Fatal("malformed URL accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("connection string leaked")
	}
}

func TestOpenBoundsWaitForUnresponsivePostgres(t *testing.T) {
	// A real TCP peer accepts connections but never speaks PostgreSQL.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			var b [1024]byte
			for {
				if _, err := conn.Read(b[:]); err != nil {
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	pool, err := Open(ctx, "postgres://ino:secret@"+listener.Addr().String()+"/ino?sslmode=disable")
	if pool != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool = %v; error = %v", pool, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("startup exceeded its timeout")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("connection string leaked")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("failed startup left a connection open")
	}
}
