package hodu

import "encoding/json"
import "errors"
import "fmt"
import "io"
import "net/http"
import "os"
import "os/exec"
import "os/user"
import "strconv"
import "strings"
import "sync"
import "syscall"
import "text/template"

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
}

// ------------------------------------------------------

func (pty *server_pty_ws) Identity() string {
	return pty.Id
}

func (pty *server_pty_ws) send_ws_data(ws *websocket.Conn, type_val string, data string) error {
	var msg []byte
	var err error

	msg, err = json.Marshal(json_xterm_ws_event{Type: type_val, Data: []string{ data } })
	if err == nil { err = websocket.Message.Send(ws, msg) }
	return err
}


func (pty *server_pty_ws) connect_pty(username string, password string) (*exec.Cmd, *os.File, error) {
	var s *Server
	var cmd *exec.Cmd
	var tty *os.File
	var err error

	// username and password are not used yet.
	s = pty.S

	if s.pty_shell == "" {
		return nil, nil, fmt.Errorf("blank pty shell")
	}

	cmd = exec.Command(s.pty_shell);
	if s.pty_user != "" {
		var uid int
		var gid int
		var u *user.User

		u, err = user.Lookup(s.pty_user)
		if err != nil { return nil, nil, err }

		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uint32(uid),
				Gid: uint32(gid),
			},
			Setsid:  true,
		}
		cmd.Dir = u.HomeDir
		cmd.Env = append(cmd.Env,
			"HOME=" + u.HomeDir,
			"LOGNAME=" + u.Username,
			"PATH=" + os.Getenv("PATH"),
			"SHELL=" + s.pty_shell,
			"TERM=xterm",
			"USER=" + u.Username,
		)
	}

	tty, err = pts.Start(cmd)
	if err != nil {
		return nil, nil, err
	}

	//syscall.SetNonblock(int(tty.Fd()), true);
	unix.SetNonblock(int(tty.Fd()), true);

	return cmd, tty, nil
}

func (pty *server_pty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var username string
	var password string
	var in *os.File
	var out *os.File
	var tty *os.File
	var cmd *exec.Cmd
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
		defer ws.Close() // dirty way to break the main loop

		conn_ready = <-conn_ready_chan
		if conn_ready { // connected
			var poll_fds []unix.PollFd;
			var buf []byte
			var n int
			var err error

			poll_fds = []unix.PollFd{
				unix.PollFd{Fd: int32(out.Fd()), Events: unix.POLLIN},
			}

			s.stats.pty_sessions.Add(1)
			buf = make([]byte, 2048)
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
					break;
				}

				if (poll_fds[0].Revents & unix.POLLIN) != 0 {
					n, err = out.Read(buf)
					if err != nil {
						if !errors.Is(err, io.EOF) {
							s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to read pty stdout - %s", req.RemoteAddr, err.Error())
						}
						break
					}
					if n > 0 {
						err = pty.send_ws_data(ws, "iov", string(buf[:n]))
						if err != nil {
							s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to send to websocket - %s", req.RemoteAddr, err.Error())
							break
						}
					}
				}
			}
			s.stats.pty_sessions.Add(-1)
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
							username = string(ev.Data[0])
							password = string(ev.Data[1])

							wg.Add(1)
							go func() {
								var err error

								defer wg.Done()
								cmd, tty, err = pty.connect_pty(username, password)
								if err != nil {
									s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to connect pty - %s", req.RemoteAddr, err.Error())
									pty.send_ws_data(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error - this will make websocket.MessageReceive to fail
								} else {
									err = pty.send_ws_data(ws, "status", "opened")
									if err != nil {
										s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to write 'opened' event to websocket - %s", req.RemoteAddr, err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										s.log.Write(pty.Id, LOG_DEBUG, "[%s] Opened pty session", req.RemoteAddr)
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
							s.log.Write(pty.Id, LOG_DEBUG, "[%s] Resized terminal to %d,%d", req.RemoteAddr, rows, cols)
							// ignore error
						}
				}
			}
		}
	}

	if tty != nil {
		err = pty.send_ws_data(ws, "status", "closed")
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
	s.log.Write(pty.Id, LOG_DEBUG, "[%s] Ended pty session", req.RemoteAddr)

	return http.StatusOK, err
}


// ------------------------------------------------------
func (rpty *server_rpty_ws) Identity() string {
	return rpty.Id
}

func (rpty *server_rpty_ws) send_ws_data(ws *websocket.Conn, type_val string, data string) error {
	var msg []byte
	var err error

	msg, err = json.Marshal(json_xterm_ws_event{Type: type_val, Data: []string{ data } })
	if err == nil { err = websocket.Message.Send(ws, msg) }
	return err
}

func (rpty *server_rpty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var token string
	var cts *ServerConn
	//var username string
	//var password string
	var in *os.File
	//var out *os.File
	var tty *os.File
	var rp *ServerRpty
	var cmd *exec.Cmd
	var wg sync.WaitGroup
	var conn_ready_chan chan bool
	var err error

	s = rpty.S
	req = ws.Request()
	conn_ready_chan = make(chan bool, 3)
	token = req.FormValue("token")
	if token != "" {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("no client token specified")
	}

	cts = s.FindServerConnByClientToken(token)
	if cts == nil {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("invalid client token - %s", token)
	}

// TODO: how to get notified of broken connection....


	wg.Add(1)
	go func() {
		var conn_ready bool

		defer wg.Done()
		defer ws.Close() // dirty way to break the main loop

		conn_ready = <-conn_ready_chan
		if conn_ready { // connected
/*
			var poll_fds []unix.PollFd;
			var buf []byte
			var n int
			var err error

			poll_fds = []unix.PollFd{
				unix.PollFd{Fd: int32(out.Fd()), Events: unix.POLLIN},
			}

			s.stats.pty_sessions.Add(1)
			buf = make([]byte, 2048)
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
					break;
				}

				if (poll_fds[0].Revents & unix.POLLIN) != 0 {
					n, err = out.Read(buf)
					if err != nil {
						if !errors.Is(err, io.EOF) {
							s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to read pty stdout - %s", req.RemoteAddr, err.Error())
						}
						break
					}
					if n > 0 {
						err = pty.send_ws_data(ws, "iov", string(buf[:n]))
						if err != nil {
							s.log.Write(pty.Id, LOG_ERROR, "[%s] Failed to send to websocket - %s", req.RemoteAddr, err.Error())
							break
						}
					}
				}
			}
			s.stats.pty_sessions.Add(-1)
*/
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
						if rp == nil && len(ev.Data) == 2 {
							//username = string(ev.Data[0])
							//password = string(ev.Data[1])

							wg.Add(1)
							go func() {
								var err error

								defer wg.Done()

								rp, err = cts.StartRpty(ws)
							// cmd, tty, err = pty.connect_pty(username, password)
								if err != nil {
									s.log.Write(rpty.Id, LOG_ERROR, "[%s] Failed to connect pty - %s", req.RemoteAddr, err.Error())
									rpty.send_ws_data(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error
								} else {
									err = rpty.send_ws_data(ws, "status", "opened")
									if err != nil {
										s.log.Write(rpty.Id, LOG_ERROR, "[%s] Failed to write 'opened' event to websocket - %s", req.RemoteAddr, err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										s.log.Write(rpty.Id, LOG_DEBUG, "[%s] Opened pty session", req.RemoteAddr)
								//		out = tty
								//		in = tty
										conn_ready_chan <- true
									}
								}
							}()
						}

					case "close":
						if tty != nil {
							// cts.StopRpty()
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
							s.log.Write(rpty.Id, LOG_DEBUG, "[%s] Resized terminal to %d,%d", req.RemoteAddr, rows, cols)
							// ignore error
						}
				}
			}
		}
	}

	if tty != nil {
		//err = pty.send_ws_data(ws, "status", "closed")
		/*
		err = s.SendRpty()
		if err != nil { goto done }
		*/
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
