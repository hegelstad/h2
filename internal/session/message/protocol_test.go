package message

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// unixPair returns a connected Unix socket pair. The listener is cleaned up
// with t.Cleanup. Both writes land in the kernel buffer before the client
// reads, which is the attach handshake race net.Pipe cannot reproduce.
func unixPair(t *testing.T) (client net.Conn, server net.Conn) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	ready := make(chan net.Conn, 1)
	errc := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		ready <- c
	}()

	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	select {
	case s := <-ready:
		t.Cleanup(func() {
			c.Close()
			s.Close()
		})
		return c, s
	case err := <-errc:
		c.Close()
		t.Fatalf("accept: %v", err)
	}
	panic("unreachable")
}

func TestReadResponse_LeavesFollowingFrame(t *testing.T) {
	client, server := unixPair(t)

	payload := bytes.Repeat([]byte("A"), 3000)
	written := make(chan struct{})
	go func() {
		if err := SendResponse(server, &Response{OK: true}); err != nil {
			t.Errorf("SendResponse: %v", err)
		}
		if err := WriteFrame(server, FrameTypeData, payload); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		close(written)
	}()
	<-written

	resp, err := ReadResponse(client)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if !resp.OK {
		t.Fatalf("ReadResponse OK=false error=%q", resp.Error)
	}

	gotType, got, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if gotType != FrameTypeData {
		t.Fatalf("frame type=%d, want %d", gotType, FrameTypeData)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("frame payload len=%d, want %d", len(got), len(payload))
	}
}

func TestReadRequest_LeavesFollowingFrame(t *testing.T) {
	client, server := unixPair(t)

	payload := bytes.Repeat([]byte("B"), 3000)
	written := make(chan struct{})
	go func() {
		if err := SendRequest(server, &Request{Type: "attach", Cols: 80, Rows: 24}); err != nil {
			t.Errorf("SendRequest: %v", err)
		}
		if err := WriteFrame(server, FrameTypeData, payload); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
		close(written)
	}()
	<-written

	req, err := ReadRequest(client)
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if req.Type != "attach" || req.Cols != 80 || req.Rows != 24 {
		t.Fatalf("request = %+v", req)
	}

	gotType, got, err := ReadFrame(client)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if gotType != FrameTypeData || !bytes.Equal(got, payload) {
		t.Fatalf("frame type=%d len=%d", gotType, len(got))
	}
}

func TestJSONDecoder_ConsumesFollowingFrame(t *testing.T) {
	// Documents why decodeJSONLine exists: encoding/json.Decoder's buffer
	// eats bytes that belong to the next attach frame.
	client, server := unixPair(t)

	payload := bytes.Repeat([]byte("C"), 3000)
	written := make(chan struct{})
	go func() {
		_ = SendResponse(server, &Response{OK: true})
		_ = WriteFrame(server, FrameTypeData, payload)
		close(written)
	}()
	<-written

	var resp Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("Decoder.Decode: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected OK")
	}
	_, _, err := ReadFrame(client)
	if err == nil {
		t.Fatal("expected ReadFrame to fail after Decoder over-read")
	}
}

func TestDecodeJSONLine_EOFWithoutNewline(t *testing.T) {
	var resp Response
	if err := decodeJSONLine(strings.NewReader(`{"ok":true}`), &resp); err != nil {
		t.Fatalf("decodeJSONLine: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected OK")
	}
}

func TestDecodeJSONLine_EmptyEOF(t *testing.T) {
	var resp Response
	if err := decodeJSONLine(strings.NewReader(""), &resp); err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want unexpected EOF", err)
	}
}

func TestDecodeJSONLine_TooLong(t *testing.T) {
	var resp Response
	r := strings.NewReader(strings.Repeat("x", maxJSONLine) + "\n")
	err := decodeJSONLine(r, &resp)
	if err == nil || !strings.Contains(err.Error(), "json line exceeds") {
		t.Fatalf("err = %v, want json line exceeds", err)
	}
}
