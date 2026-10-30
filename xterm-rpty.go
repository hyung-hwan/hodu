package hodu

import "context"
import "fmt"
import "io"
import "sync"
import "sync/atomic"

import "golang.org/x/net/websocket"

type xterm_rpty_output struct {
	chunks chan []byte
	done chan struct{}
	once sync.Once
	pending []byte
	overflow atomic.Bool
}

func (out *xterm_rpty_output) stop() {
	out.once.Do(func() { close(out.done) })
}

func (out *xterm_rpty_output) push(data []byte) {
	var copied []byte

	select {
		case <-out.done:
			return
		default:
	}
	copied = append([]byte(nil), data...)
	select {
		case out.chunks <- copied:
		default:
			out.overflow.Store(true)
			out.stop()
	}
}

func (out *xterm_rpty_output) Read(data []byte) (int, error) {
	var count int

	for len(out.pending) == 0 {
		select {
			case out.pending = <-out.chunks:
			default:
				select {
					case out.pending = <-out.chunks:
					case <-out.done:
						select {
							case out.pending = <-out.chunks:
							default:
								if out.overflow.Load() { return 0, fmt.Errorf("remote terminal output queue full") }
								return 0, io.EOF
						}
				}
		}
	}
	count = copy(data, out.pending)
	out.pending = out.pending[count:]
	return count, nil
}

func (cts *ServerConn) open_xterm_rpty(ctx context.Context, ws *websocket.Conn) (*xterm_backend, error) {
	var out *xterm_rpty_output
	var rpty *ServerRpty
	var once sync.Once
	var err error

	if ctx.Err() != nil { return nil, ctx.Err() }
	out = &xterm_rpty_output{chunks: make(chan []byte, 64), done: make(chan struct{})}
	rpty, err = cts.start_rpty(ws, out.push, out.stop)
	if err != nil { return nil, err }
	return &xterm_backend{
		in: &xterm_rpty_input{cts: cts, id: rpty.id},
		out: out,
		resize: func(rows int, cols int) error {
			return cts.pss.Send(MakeRptySizePacket(rpty.id, []byte(fmt.Sprintf("%d %d", rows, cols))))
		},
		close: func() {
			once.Do(func() {
				out.stop()
				cts.rpty_mtx.Lock()
				delete(cts.rpty_map, rpty.id)
				cts.rpty_mtx.Unlock()
				cts.S.stats.rpty_sessions.Add(-1)
				cts.pss.Send(MakeRptyStopPacket(rpty.id, ""))
			})
		},
		wait: func() {},
	}, nil
}

type xterm_rpty_input struct {
	cts *ServerConn
	id uint64
}

func (in *xterm_rpty_input) Write(data []byte) (int, error) {
	var err error
	err = in.cts.pss.Send(MakeRptyDataPacket(in.id, data))
	if err != nil { return 0, err }
	return len(data), nil
}
