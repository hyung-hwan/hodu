package hodu

import "encoding/json"
import "errors"
import "io"
import "net/http"
import "os"
import "os/exec"
import "strconv"
import "strings"
import "sync"
import "text/template"

import pts "github.com/creack/pty"
import "golang.org/x/net/websocket"
import "golang.org/x/sys/unix"

type client_pty_ws struct {
	C *Client
	Id string
	ws *websocket.Conn
}

type client_pty_xterm_file struct {
	client_ctl
	file string
}

// ------------------------------------------------------

func (pty *client_pty_ws) Identity() string {
	return pty.Id
}

func (pty *client_pty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var c *Client
	var req *http.Request
	//var username string
	//var password string
	var in *os.File
	var out *os.File
	var tty *os.File
	var cmd *exec.Cmd
	var wg sync.WaitGroup
	var conn_ready_chan chan bool
	var err error

	c = pty.C
	req = ws.Request()
	conn_ready_chan = make(chan bool, 3)

	wg.Add(1)
	go func() {
		var conn_ready bool

		defer wg.Done()
		defer ws.Close() // dirty way to break the main loop

		conn_ready = <-conn_ready_chan
		if conn_ready { // connected
			var poll_fds []unix.PollFd
			var buf [2048]byte
			var n int
			var err error


			poll_fds = []unix.PollFd{
				unix.PollFd{Fd: int32(out.Fd()), Events: unix.POLLIN},
			}

			c.stats.pty_sessions.Add(1)
			for {
				n, err = unix.Poll(poll_fds, -1) // -1 means wait indefinitely
				if err != nil {
					if errors.Is(err, unix.EINTR) { continue }
					c.log.Write("", LOG_ERROR, "[%s] Failed to poll pty stdout - %s", req.RemoteAddr, err.Error())
					break
				}
				if n == 0 { // timed out
					continue
				}

				if (poll_fds[0].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
					c.log.Write(pty.Id, LOG_DEBUG, "[%s] EOF detected on pty stdout", req.RemoteAddr)
					break
				}

				if (poll_fds[0].Revents & unix.POLLIN) != 0 {
					n, err = out.Read(buf[:])
					if n > 0 {
						var err2 error
						err2 = send_ws_data_for_xterm(ws, "iov", string(buf[:n]))
						if err2 != nil {
							c.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to send to websocket - %s", req.RemoteAddr, err2.Error())
							break
						}
					}
					if err != nil {
						if !errors.Is(err, io.EOF) {
							c.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to read pty stdout - %s", req.RemoteAddr, err.Error())
						}
						break
					}
				}
			}
			c.stats.pty_sessions.Add(-1)
		}
	}()

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil { goto done }

		if len(msg) > 0 {
			var ev json_xterm_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if tty == nil && len(ev.Data) == 2 {
							//username = string(ev.Data[0])
							//password = string(ev.Data[1])

							wg.Add(1)
							go func() {
								var err error

								defer wg.Done()
								cmd, tty, err = connect_pty(c.pty_shell, c.pty_user)
								if err != nil {
									c.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to connect pty - %s", req.RemoteAddr, err.Error())
									send_ws_data_for_xterm(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error
								} else {
									err = send_ws_data_for_xterm(ws, "status", "opened")
									if err != nil {
										c.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to write opened event to websocket - %s", req.RemoteAddr, err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										c.log.Write(pty.Id, LOG_DEBUG, "[%s] Opened pty session", req.RemoteAddr)
										out = tty
										in = tty
										conn_ready_chan <- true
									}
								}
							}()
						}

					case "close":
						if tty != nil {
							tty.Close()
							tty = nil
						}
						break ws_recv_loop

					case "iov":
						if tty != nil {
							var i int
							for i, _ = range ev.Data {
								in.Write([]byte(ev.Data[i]))
							}
						}

					case "size":
						if tty != nil && len(ev.Data) == 2 {
							var rows int
							var cols int
							rows, _ = strconv.Atoi(ev.Data[0])
							cols, _ = strconv.Atoi(ev.Data[1])
							pts.Setsize(tty, &pts.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
							c.log.Write(pty.Id, LOG_DEBUG, "[%s] Resized terminal to %d,%d", req.RemoteAddr, rows, cols)
							// ignore error
						}
				}
			}
		}
	}

	if tty != nil {
		err = send_ws_data_for_xterm(ws, "status", "closed")
		if err != nil { goto done }
	}

done:
	conn_ready_chan <- false
	ws.Close()
	if cmd != nil {
		// kill the child process underneath to close ptym(the master pty).
		//cmd.Process.Signal(syscall.SIGTERM)
		cmd.Process.Kill()
	}
	if tty != nil { tty.Close() }
	if cmd != nil { cmd.Wait() }
	wg.Wait()
	c.log.Write(pty.Id, LOG_DEBUG, "[%s] Ended pty session", req.RemoteAddr)

	return http.StatusOK, err
}


// ------------------------------------------------------

func (pty *client_pty_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var err error

	c = pty.c

	switch pty.file {
		case "xterm.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_js)
		case "xterm-addon-fit.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_addon_fit_js)
		case "xterm.css":
			status_code = WriteCssRespHeader(w, http.StatusOK)
			w.Write(xterm_css)
		case "xterm.html":
			var tmpl *template.Template

			tmpl = template.New("")
			if c.xterm_html !=  "" {
				_, err = tmpl.Parse(c.xterm_html)
			} else {
				_, err = tmpl.Parse(xterm_html)
			}
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusInternalServerError)
				goto oops
			} else {
				status_code = WriteHtmlRespHeader(w, http.StatusOK)
				tmpl.Execute(w,
					&xterm_session_info{
						Mode: "pty",
						ConnId: "-1",
						RouteId: "-1",
					})
			}


		case "_forbidden":
			status_code = WriteEmptyRespHeader(w, http.StatusForbidden)

		case "_notfound":
			status_code = WriteEmptyRespHeader(w, http.StatusNotFound)

		default:
			if strings.HasPrefix(pty.file, "_redir:") {
				status_code = http.StatusMovedPermanently
				w.Header().Set("Location", pty.file[7:])
				w.WriteHeader(status_code)
			} else {
				status_code = WriteEmptyRespHeader(w, http.StatusNotFound)
			}
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}
