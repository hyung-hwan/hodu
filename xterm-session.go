package hodu

import "context"
import "encoding/base64"
import "encoding/json"
import "errors"
import "fmt"
import "io"
import "net/http"
import "os"
import "os/exec"
import "strconv"
import "sync"
import "time"

import pts "github.com/creack/pty"
import "golang.org/x/net/websocket"
import "golang.org/x/sys/unix"

const xterm_session_max = 32
const xterm_message_max = 64 * 1024

type xterm_backend struct {
	in io.Writer
	out io.Reader
	resize func(int, int) error
	close func()
	wait func()
}

type xterm_session struct {
	cancel context.CancelFunc
	input chan json_xterm_ws_event
}

type xterm_connection struct {
	ws *websocket.Conn
	ctx context.Context
	cancel context.CancelFunc
	output chan json_xterm_ws_event
	mtx sync.Mutex
	sessions map[string]*xterm_session
	wg sync.WaitGroup
	open func(context.Context, []string) (*xterm_backend, error)
}

func (conn *xterm_connection) send(id string, kind string, data string) {
	select {
		case <-conn.ctx.Done():
			return
		default:
	}
	select {
		case conn.output <- json_xterm_ws_event{Session: id, Type: kind, Data: []string{data}}:
		case <-conn.ctx.Done():
		default:
			conn.cancel()
			conn.ws.Close()
	}
}

func (conn *xterm_connection) run_session(ctx context.Context, id string, session *xterm_session, data []string) {
	var backend *xterm_backend
	var read_done chan error
	var stop_close func() bool
	var err error

	defer conn.wg.Done()
	defer func() {
		session.cancel()
		conn.mtx.Lock()
		delete(conn.sessions, id)
		conn.mtx.Unlock()
		conn.send(id, "status", "closed")
		if id == "" { conn.ws.SetReadDeadline(time.Now()) }
	}()

	backend, err = conn.open(ctx, data)
	if err != nil {
		if ctx.Err() == nil { conn.send(id, "error", err.Error()) }
		return
	}
	read_done = make(chan error, 1)
	defer backend.wait()
	defer backend.close()
	stop_close = context.AfterFunc(ctx, backend.close)
	defer stop_close()
	conn.send(id, "status", "opened")
	go func() {
		var buf [4096]byte
		var count int
		var read_err error

		for {
			count, read_err = backend.out.Read(buf[:])
			if count > 0 { conn.send(id, "iov", base64.StdEncoding.EncodeToString(buf[:count])) }
			if read_err != nil {
				read_done <- read_err
				return
			}
		}
	}()

	for {
		var event json_xterm_ws_event

		select {
			case <-ctx.Done():
				backend.close()
				<-read_done
				return
			case err = <-read_done:
				if !errors.Is(err, io.EOF) && !errors.Is(err, unix.EIO) && ctx.Err() == nil {
					conn.send(id, "error", err.Error())
				}
				return
			case event = <-session.input:
				err = nil
				switch event.Type {
					case "iov":
						var encoded string
						for _, encoded = range event.Data {
							var decoded []byte
							decoded, err = base64.StdEncoding.DecodeString(encoded)
							if err == nil { _, err = backend.in.Write(decoded) }
							if err != nil { break }
						}
					case "size":
						var rows int
						var cols int
						if len(event.Data) != 2 {
							err = fmt.Errorf("expected rows and columns")
						} else {
							rows, err = strconv.Atoi(event.Data[0])
							if err == nil { cols, err = strconv.Atoi(event.Data[1]) }
							if err == nil && (rows < 1 || rows > 65535 || cols < 1 || cols > 65535) {
								err = fmt.Errorf("invalid terminal size")
							}
							if err == nil { err = backend.resize(rows, cols) }
						}
				}
				if err != nil { conn.send(id, "error", err.Error()) }
		}
	}
}

func serve_xterm_sessions(ws *websocket.Conn, open func(context.Context, []string) (*xterm_backend, error), shutdown ...func()) (int, error) {
	var conn xterm_connection
	var writer_done chan struct{}
	var stop func()
	var err error

	conn.ws = ws
	conn.ctx, conn.cancel = context.WithCancel(ws.Request().Context())
	conn.output = make(chan json_xterm_ws_event, 128)
	conn.sessions = make(map[string]*xterm_session)
	conn.open = open
	writer_done = make(chan struct{})
	ws.MaxPayloadBytes = xterm_message_max
	go func() {
		defer close(writer_done)
		for {
			var event json_xterm_ws_event
			select {
				case <-conn.ctx.Done():
					return
				case event = <-conn.output:
					ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
					if websocket.JSON.Send(ws, event) != nil {
						conn.cancel()
						ws.Close()
						return
					}
			}
		}
	}()
	for {
		var message []byte
		var event json_xterm_ws_event
		var session *xterm_session

		err = websocket.Message.Receive(ws, &message)
		if err != nil { break }
		if json.Unmarshal(message, &event) != nil || len(event.Session) > 64 {
			conn.send("", "error", "invalid terminal message")
			continue
		}
		conn.mtx.Lock()
		session = conn.sessions[event.Session]
		if event.Type == "open" {
			var ctx context.Context

			if session != nil || len(conn.sessions) >= xterm_session_max || len(event.Data) != 2 {
				conn.mtx.Unlock()
				conn.send(event.Session, "error", "session already exists, limit reached, or invalid open request")
				continue
			}
			ctx, session = new_xterm_session(conn.ctx)
			conn.sessions[event.Session] = session
			conn.wg.Add(1)
			go conn.run_session(ctx, event.Session, session, event.Data)
			conn.mtx.Unlock()
			continue
		}
		conn.mtx.Unlock()
		if session == nil {
			conn.send(event.Session, "error", "unknown terminal session")
			continue
		}
		if event.Type == "close" {
			session.cancel()
		} else if event.Type == "iov" || event.Type == "size" {
			select {
				case session.input <- event:
				default:
					conn.send(event.Session, "error", "terminal input queue full")
					session.cancel()
			}
		} else {
			conn.send(event.Session, "error", "unknown terminal event")
		}
	}
	conn.cancel()
	ws.Close()
	for _, stop = range shutdown { stop() }
	conn.wg.Wait()
	<-writer_done
	return http.StatusOK, err
}

func new_xterm_session(parent context.Context) (context.Context, *xterm_session) {
	var ctx context.Context
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(parent)
	return ctx, &xterm_session{cancel: cancel, input: make(chan json_xterm_ws_event, 32)}
}

type xterm_pty_reader struct {
	ctx context.Context
	tty *os.File
}

func (reader *xterm_pty_reader) Read(data []byte) (int, error) {
	var fds []unix.PollFd
	var count int
	var err error

	fds = []unix.PollFd{{Fd: int32(reader.tty.Fd()), Events: unix.POLLIN}}
	for {
		if reader.ctx.Err() != nil { return 0, io.EOF }
		count, err = unix.Poll(fds, 200)
		if errors.Is(err, unix.EINTR) { continue }
		if err != nil { return 0, err }
		if count == 0 { continue }
		if fds[0].Revents & unix.POLLIN != 0 {
			count, err = unix.Read(int(reader.tty.Fd()), data)
			if errors.Is(err, unix.EAGAIN) { continue }
			return count, err
		}
		return 0, io.EOF
	}
}

func open_xterm_pty(ctx context.Context, shell string, user string, count func(int64)) (*xterm_backend, error) {
	var cmd *exec.Cmd
	var tty *os.File
	var once sync.Once
	var err error

	if ctx.Err() != nil { return nil, ctx.Err() }
	cmd, tty, err = connect_pty(shell, user)
	if err != nil { return nil, err }
	count(1)
	return &xterm_backend{
		in: tty,
		out: &xterm_pty_reader{ctx: ctx, tty: tty},
		resize: func(rows int, cols int) error {
			return pts.Setsize(tty, &pts.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
		},
		close: func() { once.Do(func() { cmd.Process.Kill() }) },
		wait: func() { tty.Close(); cmd.Wait(); count(-1) },
	}, nil
}
