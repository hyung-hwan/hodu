package hodu

import "encoding/base64"
import "encoding/json"
import "errors"
import "fmt"
import "io"
import "net/http"
import "os"
import "os/exec"
import "strconv"
import "strings"
import "sync"
import "text/template"
import "time"

import pts "github.com/creack/pty"
import "golang.org/x/net/websocket"
import "golang.org/x/sys/unix"

type server_pty_ws struct {
	S *Server
	Id string
	ws *websocket.Conn
}

type server_rpty_ws struct {
	S *Server
	Id string
	ws *websocket.Conn
}

type server_pty_xterm_file struct {
	ServerCtl
	file string
	mode string
}

// ------------------------------------------------------

func (pty *server_pty_ws) Identity() string {
	return pty.Id
}

func (pty *server_pty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	//var username string
	//var password string
	var in *os.File
	var out *os.File
	var tty *os.File
	var cmd *exec.Cmd
	var pfd [2]int = [2]int{ -1, -1 }
	var wg sync.WaitGroup
	var conn_ready_chan chan bool
	var err error

	s = pty.S
	req = ws.Request()
	conn_ready_chan = make(chan bool, 3)

	wg.Add(1)
	go func() {
		var conn_ready bool

		defer wg.Done()
		//defer ws.Close() // dirty way to break the main loop
		defer ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop

		conn_ready = <-conn_ready_chan
		if conn_ready { // connected
			var poll_fds []unix.PollFd
			var buf [2048]byte
			var n int
			var err error

			poll_fds = []unix.PollFd{
				unix.PollFd{Fd: int32(out.Fd()), Events: unix.POLLIN},
				unix.PollFd{Fd: int32(pfd[0]), Events: unix.POLLIN},
			}

			s.stats.pty_sessions.Add(1)
			for {
				n, err = unix.Poll(poll_fds, -1) // -1 means wait indefinitely
				if err != nil {
					if errors.Is(err, unix.EINTR) { continue }
					s.log.Write("", LOG_ERROR, "[%s] Failed to poll pty stdout - %s", req.RemoteAddr, err.Error())
					break
				}
				if n == 0 { // timed out
					continue
				}

				if (poll_fds[0].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
					s.log.Write(pty.Id, LOG_DEBUG, "[%s] EOF detected on pty stdout", req.RemoteAddr)
					break
				}
				if (poll_fds[1].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
					s.log.Write(pty.Id, LOG_DEBUG, "[%s] EOF detected on pty event pipe", req.RemoteAddr)
					break
				}

				if (poll_fds[0].Revents & unix.POLLIN) != 0 {
					n, err = out.Read(buf[:])
					if n > 0 {
						var err2 error
						err2 = send_ws_data_for_xterm(ws, "iov", base64.StdEncoding.EncodeToString(buf[:n]))
						if err2 != nil {
							s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to send to websocket - %s", req.RemoteAddr, err2.Error())
							break
						}
					}
					if err != nil {
						if !errors.Is(err, io.EOF) {
							s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to read pty stdout - %s", req.RemoteAddr, err.Error())
						}
						break
					}
				}
				if (poll_fds[1].Revents & unix.POLLIN) != 0 {
					s.log.Write(pty.Id, LOG_DEBUG, "[%s] Stop request noticed on pty event pipe", req.RemoteAddr)
					break
				}
			}
			s.stats.pty_sessions.Add(-1)
		}
	}()

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil {
			s.log.Write(pty.Id, LOG_ERROR, "[%s] websocket receive error - %s", req.RemoteAddr, err.Error())
			goto done
		}

		if len(msg) > 0 {
			var ev json_xterm_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if tty == nil && len(ev.Data) == 2 {
							// not using username and password for now...
							//username = string(ev.Data[0])
							//password = string(ev.Data[1])

							wg.Add(1)
							go func() {
								var err error

								defer wg.Done()

								err = unix.Pipe(pfd[:])
								if err != nil {
									s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to create event pipe for pty - %s", req.RemoteAddr, err.Error())
									send_ws_data_for_xterm(ws, "error", err.Error())
									//ws.Close() // dirty way to flag out the error
									ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
									return
								}

								cmd, tty, err = connect_pty(s.pty_shell, s.pty_user)
								if err != nil {
									s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to connect pty - %s", req.RemoteAddr, err.Error())
									send_ws_data_for_xterm(ws, "error", err.Error())
									//ws.Close() // dirty way to flag out the error - this will make websocket.MessageReceive to fail
									ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
									unix.Close(pfd[0]); pfd[0] = -1
									unix.Close(pfd[1]); pfd[1] = -1
									return
								}

								err = send_ws_data_for_xterm(ws, "status", "opened")
								if err != nil {
									s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to write 'opened' event to websocket - %s", req.RemoteAddr, err.Error())
									//ws.Close() // dirty way to flag out the error
									ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
									unix.Close(pfd[0]); pfd[0] = -1
									unix.Close(pfd[1]); pfd[1] = -1
									return
								}

								s.log.Write(pty.Id, LOG_DEBUG, "[%s] Opened pty session", req.RemoteAddr)
								out = tty
								in = tty
								conn_ready_chan <- true
							}()
						}

					case "close":
						if tty != nil {
							tty.Close()
							tty = nil
						}
						if pfd[1] >= 0 {
							unix.Write(pfd[1], []byte{0})
						}
						break ws_recv_loop

					case "iov":
						if tty != nil {
							var i int
							for i, _ = range ev.Data {
								//in.Write([]byte(ev.Data[i]))
								var bytes []byte
								bytes, err = base64.StdEncoding.DecodeString(ev.Data[i])
								if err != nil {
									s.log.Write(pty.Id, LOG_WARN, "[%s] Invalid pty iov data received - %s", req.RemoteAddr, ev.Data[i])
								} else {
									in.Write(bytes)
								}
							}
						}

					case "size":
						if tty != nil && len(ev.Data) == 2 {
							var rows int
							var cols int
							rows, _ = strconv.Atoi(ev.Data[0])
							cols, _ = strconv.Atoi(ev.Data[1])
							pts.Setsize(tty, &pts.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
							s.log.Write(pty.Id, LOG_DEBUG, "[%s] Resized terminal to %d,%d", req.RemoteAddr, rows, cols)
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

	// close the event pipe after all goroutines are over
	if pfd[0] >= 0 { unix.Close(pfd[0]) }
	if pfd[1] >= 0 { unix.Close(pfd[1]) }

	s.log.Write(pty.Id, LOG_DEBUG, "[%s] Ended pty session", req.RemoteAddr)

	return http.StatusOK, err
}


// ------------------------------------------------------
func (rpty *server_rpty_ws) Identity() string {
	return rpty.Id
}

func (rpty *server_rpty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var token string
	var cts *ServerConn
	//var username string
	//var password string
	var rp *ServerRpty
	var wg sync.WaitGroup
	var err error

	s = rpty.S
	req = ws.Request()
	token = req.FormValue("client-token")
	if token == "" {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("no client token specified")
	}

	cts = s.FindServerConnByClientToken(token)
	if cts == nil {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("invalid client token - %s", token)
	}

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil {
			s.log.Write(rpty.Id, LOG_ERROR, "[%s] websocket receive error - %s", req.RemoteAddr, err.Error())
			goto done
		}

		if len(msg) > 0 {
			var ev json_xterm_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if rp == nil && len(ev.Data) == 2 {
							//username = string(ev.Data[0])
							//password = string(ev.Data[1])

							rp, err = cts.StartRpty(ws)
							if err != nil {
								s.log.Write(rpty.Id, LOG_ERROR, "[%s] Failed to connect rpty - %s", req.RemoteAddr, err.Error())
								send_ws_data_for_xterm(ws, "error", err.Error())
								//ws.Close() // dirty way to flag out the error by making websocket.Message.Receive() fail
								ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
							} else {
								err = send_ws_data_for_xterm(ws, "status", "opened")
								if err != nil {
									s.log.Write(rpty.Id, LOG_ERROR, "[%s] Failed to write 'opened' event to websocket - %s", req.RemoteAddr, err.Error())
									//ws.Close() // dirty way to flag out the error
									ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
								} else {
									s.log.Write(rpty.Id, LOG_DEBUG, "[%s] Opened rpty session", req.RemoteAddr)
								}
							}
						}

					case "close":
						// just break out of the loop and let the remainder to close resources
						break ws_recv_loop

					case "iov":
						var i int
						for i, _ = range ev.Data {
							//cts.WriteRpty(ws, []byte(ev.Data[i]))
							var bytes []byte
							bytes, err = base64.StdEncoding.DecodeString(ev.Data[i])
							if err != nil {
								s.log.Write(rpty.Id, LOG_WARN, "[%s] Invalid rpty iov data received - %s", req.RemoteAddr, ev.Data[i])
							} else {
								cts.WriteRpty(ws, bytes)
								// ignore error for now
							}
						}

					case "size":
						if len(ev.Data) == 2 {
							cts.WriteRptySize(ws, []byte(fmt.Sprintf("%s %s", ev.Data[0], ev.Data[1])))
							s.log.Write(rpty.Id, LOG_DEBUG, "[%s] Requested to resize rpty terminal to %s,%s", req.RemoteAddr, ev.Data[0], ev.Data[1])
							// ignore error
						}
				}
			}
		}
	}

done:
	cts.StopRpty(ws)
	ws.Close() // don't care about multiple closes

	wg.Wait()
	s.log.Write(rpty.Id, LOG_DEBUG, "[%s] Ended rpty session for %s", req.RemoteAddr, token)

	return http.StatusOK, err
}

// ------------------------------------------------------

func (pty *server_pty_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var err error

	s = pty.S

	switch pty.file {
		case "xterm.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_js)
		case "xterm-addon-fit.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_addon_fit_js)
		case "xterm-addon-unicode11.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_addon_unicode11_js)
		case "xterm.css":
			status_code = WriteCssRespHeader(w, http.StatusOK)
			w.Write(xterm_css)
		case "xterm.html":
			var tmpl *template.Template

			tmpl = template.New("")
			if s.xterm_html !=  "" {
				_, err = tmpl.Parse(s.xterm_html)
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
						Mode: pty.mode,
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
