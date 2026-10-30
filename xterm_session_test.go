package hodu

import "context"
import "crypto/ed25519"
import "crypto/rand"
import "encoding/base64"
import "io"
import "net"
import "net/http/httptest"
import "strings"
import "sync"
import "sync/atomic"
import "testing"
import "time"

import "golang.org/x/crypto/ssh"
import "golang.org/x/net/websocket"

func xterm_test_socket(t *testing.T, open func(context.Context, []string) (*xterm_backend, error), shutdown ...func()) *websocket.Conn {
	var server *httptest.Server
	var socket *websocket.Conn
	var done chan struct{}
	var err error

	t.Helper()
	done = make(chan struct{})
	server = httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer close(done)
		serve_xterm_sessions(ws, open, shutdown...)
	}))
	socket, err = websocket.Dial("ws" + strings.TrimPrefix(server.URL, "http"), "", server.URL)
	if err != nil { server.Close(); t.Fatal(err) }
	t.Cleanup(func() {
		socket.Close()
		select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("terminal handler did not stop")
		}
		server.Close()
	})
	return socket
}

func xterm_test_send(t *testing.T, socket *websocket.Conn, id string, kind string, data ...string) {
	var err error

	t.Helper()
	socket.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = websocket.JSON.Send(socket, json_xterm_ws_event{Session: id, Type: kind, Data: data})
	if err != nil { t.Fatal(err) }
}

func xterm_test_expect(t *testing.T, socket *websocket.Conn, id string, kind string, wanted string) {
	var event json_xterm_ws_event
	var output string
	var err error

	t.Helper()
	socket.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		err = websocket.JSON.Receive(socket, &event)
		if err != nil { t.Fatalf("waiting for %s/%s/%q: %v", id, kind, wanted, err) }
		if event.Type == "error" { t.Fatalf("session %s: %v", event.Session, event.Data) }
		if event.Session != id || event.Type != kind { continue }
		if kind == "iov" {
			var decoded []byte
			decoded, err = base64.StdEncoding.DecodeString(event.Data[0])
			if err != nil { t.Fatal(err) }
			output += string(decoded)
			if strings.Contains(output, wanted) { return }
		} else if len(event.Data) > 0 && event.Data[0] == wanted {
			return
		}
	}
}

func TestXtermMultiplePtys(t *testing.T) {
	var socket *websocket.Conn
	var sessions atomic.Int64

	socket = xterm_test_socket(t, func(ctx context.Context, data []string) (*xterm_backend, error) {
		return open_xterm_pty(ctx, "/bin/sh", "", func(delta int64) { sessions.Add(delta) })
	})
	xterm_test_send(t, socket, "one", "open", "", "")
	xterm_test_expect(t, socket, "one", "status", "opened")
	xterm_test_send(t, socket, "two", "open", "", "")
	xterm_test_expect(t, socket, "two", "status", "opened")
	if sessions.Load() != 2 { t.Fatal("expected two live PTYs") }
	xterm_test_send(t, socket, "one", "size", "35", "91")
	xterm_test_send(t, socket, "one", "iov", base64.StdEncoding.EncodeToString([]byte("stty size\n")))
	xterm_test_expect(t, socket, "one", "iov", "35 91")
	xterm_test_send(t, socket, "one", "close")
	xterm_test_expect(t, socket, "one", "status", "closed")
	if sessions.Load() != 1 { t.Fatal("closing one panel affected another PTY") }
	xterm_test_send(t, socket, "two", "iov", base64.StdEncoding.EncodeToString([]byte("printf '__survivor__\\n'\n")))
	xterm_test_expect(t, socket, "two", "iov", "__survivor__\r\n")
	xterm_test_send(t, socket, "two", "iov", base64.StdEncoding.EncodeToString([]byte("exit\n")))
	xterm_test_expect(t, socket, "two", "status", "closed")
	xterm_test_send(t, socket, "three", "open", "", "")
	xterm_test_expect(t, socket, "three", "status", "opened")
	socket.Close()
}

func TestXtermLegacySession(t *testing.T) {
	var socket *websocket.Conn
	var event json_xterm_ws_event
	var err error

	socket = xterm_test_socket(t, func(ctx context.Context, data []string) (*xterm_backend, error) {
		return open_xterm_pty(ctx, "/bin/sh", "", func(delta int64) {})
	})
	xterm_test_send(t, socket, "", "open", "", "")
	xterm_test_expect(t, socket, "", "status", "opened")
	xterm_test_send(t, socket, "", "close")
	socket.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		err = websocket.JSON.Receive(socket, &event)
		if err != nil { break }
		if event.Session != "" { t.Fatal("legacy response has a session ID") }
	}
}

func TestXtermCancelPendingSession(t *testing.T) {
	var socket *websocket.Conn
	var opening chan struct{}

	opening = make(chan struct{})
	socket = xterm_test_socket(t, func(ctx context.Context, data []string) (*xterm_backend, error) {
		if data[0] == "pending" {
			close(opening)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return open_xterm_pty(ctx, "/bin/sh", "", func(delta int64) {})
	})
	xterm_test_send(t, socket, "pending", "open", "pending", "")
	select {
		case <-opening:
		case <-time.After(5 * time.Second):
			t.Fatal("session factory not called")
	}
	xterm_test_send(t, socket, "pending", "close")
	for {
		var event json_xterm_ws_event
		var err error
		socket.SetReadDeadline(time.Now().Add(5 * time.Second))
		err = websocket.JSON.Receive(socket, &event)
		if err != nil { t.Fatal(err) }
		if event.Type == "status" && event.Data[0] == "closed" { break }
	}
	xterm_test_send(t, socket, "next", "open", "", "")
	xterm_test_expect(t, socket, "next", "status", "opened")
}

type xterm_test_rpty_stream struct {
	Hodu_PacketStreamServer
	packets chan *Packet
}

func (stream *xterm_test_rpty_stream) Send(packet *Packet) error {
	stream.packets <- packet
	return nil
}

func TestXtermMultipleRemotePtys(t *testing.T) {
	var server Server
	var cts ServerConn
	var stream *xterm_test_rpty_stream
	var first *xterm_backend
	var second *xterm_backend
	var first_id uint64
	var second_id uint64
	var bytes [3]byte
	var err error

	stream = &xterm_test_rpty_stream{packets: make(chan *Packet, 16)}
	cts.S = &server
	cts.pss = &GuardedPacketStreamServer{Hodu_PacketStreamServer: stream}
	cts.rpty_map = make(ServerRptyMap)
	cts.rpty_map_by_ws = make(ServerRptyMapByWs)
	cts.rpty_next_id = 1
	first, err = cts.open_xterm_rpty(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	defer first.close()
	first_id = (<-stream.packets).GetRptyEvt().Id
	second, err = cts.open_xterm_rpty(context.Background(), nil)
	if err != nil { t.Fatal(err) }
	defer second.close()
	second_id = (<-stream.packets).GetRptyEvt().Id
	if first_id == second_id { t.Fatal("remote PTYs share an ID") }
	cts.ReadRptyAndWriteWs(first_id, []byte("one"))
	cts.ReadRptyAndWriteWs(second_id, []byte("two"))
	_, err = io.ReadFull(first.out, bytes[:])
	if err != nil || string(bytes[:]) != "one" { t.Fatal("first PTY output routing failed", err) }
	first.close()
	_, err = io.ReadFull(second.out, bytes[:])
	if err != nil || string(bytes[:]) != "two" { t.Fatal("second PTY output lost", err) }
	if len(cts.rpty_map) != 1 || server.stats.rpty_sessions.Load() != 1 { t.Fatal("remote PTY cleanup affected sibling") }
	cts.rpty_map[second_id].ReqStop()
	_, err = second.out.Read(bytes[:])
	if err != io.EOF { t.Fatal("remote stop did not end the session", err) }
}

func TestXtermSharedSshConnection(t *testing.T) {
	var listener net.Listener
	var key ed25519.PrivateKey
	var signer ssh.Signer
	var config ssh.ServerConfig
	var pool xterm_ssh_pool
	var socket *websocket.Conn
	var accepted atomic.Int64
	var workers sync.WaitGroup
	var err error

	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil { t.Fatal(err) }
	signer, err = ssh.NewSignerFromKey(key)
	if err != nil { t.Fatal(err) }
	config.PasswordCallback = func(metadata ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) { return nil, nil }
	config.AddHostKey(signer)
	listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			var connection net.Conn
			var accept_err error
			connection, accept_err = listener.Accept()
			if accept_err != nil { return }
			accepted.Add(1)
			workers.Add(1)
			go func(raw net.Conn) {
				var transport *ssh.ServerConn
				var channels <-chan ssh.NewChannel
				var requests <-chan *ssh.Request
				var channel_request ssh.NewChannel
				var handshake_err error

				defer workers.Done()
				defer raw.Close()
				transport, channels, requests, handshake_err = ssh.NewServerConn(raw, &config)
				if handshake_err != nil { return }
				defer transport.Close()
				go ssh.DiscardRequests(requests)
				for channel_request = range channels {
					var channel ssh.Channel
					var session_requests <-chan *ssh.Request
					channel, session_requests, handshake_err = channel_request.Accept()
					if handshake_err != nil { continue }
					workers.Add(1)
					go func(channel ssh.Channel, requests <-chan *ssh.Request) {
						var request *ssh.Request
						defer workers.Done()
						defer channel.Close()
						for request = range requests {
							request.Reply(true, nil)
							if request.Type == "shell" {
								go ssh.DiscardRequests(requests)
								io.Copy(channel, channel)
								return
							}
						}
					}(channel, session_requests)
				}
			}(connection)
		}
	}()
	pool.route = &ServerRoute{SvcAddr: listener.Addr().(*net.TCPAddr)}
	pool.clients = make(map[xterm_ssh_credentials]*ssh.Client)
	pool.count = func(delta int64) {}
	t.Cleanup(func() { pool.close(); listener.Close(); workers.Wait() })
	socket = xterm_test_socket(t, pool.open, pool.close)
	xterm_test_send(t, socket, "one", "open", "user", "password")
	xterm_test_expect(t, socket, "one", "status", "opened")
	xterm_test_send(t, socket, "two", "open", "user", "password")
	xterm_test_expect(t, socket, "two", "status", "opened")
	if accepted.Load() != 1 { t.Fatalf("expected one SSH connection, got %d", accepted.Load()) }
	xterm_test_send(t, socket, "one", "iov", base64.StdEncoding.EncodeToString([]byte("first")))
	xterm_test_expect(t, socket, "one", "iov", "first")
	xterm_test_send(t, socket, "one", "close")
	xterm_test_expect(t, socket, "one", "status", "closed")
	xterm_test_send(t, socket, "two", "iov", base64.StdEncoding.EncodeToString([]byte("second")))
	xterm_test_expect(t, socket, "two", "iov", "second")
}
