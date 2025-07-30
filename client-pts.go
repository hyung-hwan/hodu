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

import "github.com/creack/pty"
import "golang.org/x/net/websocket"
import "golang.org/x/sys/unix"

type client_pts_ws struct {
	C *Client
	Id string
	ws *websocket.Conn
}

type client_pts_xterm_file struct {
	client_ctl
	file string
}

// ------------------------------------------------------

func (pts *client_pts_ws) Identity() string {
	return pts.Id
}

func (pts *client_pts_ws) send_ws_data(ws *websocket.Conn, type_val string, data string) error {
	var msg []byte
	var err error

	msg, err = json.Marshal(json_xterm_ws_event{Type: type_val, Data: []string{ data } })
	if err == nil { err = websocket.Message.Send(ws, msg) }
	return err
}


func (pts *client_pts_ws) connect_pts(username string, password string) (*exec.Cmd, *os.File, error) {
	var c *Client
	var cmd *exec.Cmd
	var tty *os.File
	var err error

	// username and password are not used yet.
	c = pts.C

	if c.pts_shell == "" {
		return nil, nil, fmt.Errorf("blank pts shell")
	}

	cmd = exec.Command(c.pts_shell);
	if c.pts_user != "" {
		var uid int
		var gid int
		var u *user.User

		u, err = user.Lookup(c.pts_user)
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
			"SHELL=" + c.pts_shell,
			"TERM=xterm",
			"USER=" + u.Username,
		)
	}

	tty, err = pty.Start(cmd)
	if err != nil {
		return nil, nil, err
	}

	//syscall.SetNonblock(int(tty.Fd()), true);
	unix.SetNonblock(int(tty.Fd()), true);

	return cmd, tty, nil
}

func (pts *client_pts_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var c *Client
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

	c = pts.C
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

			c.stats.pts_sessions.Add(1)
			buf = make([]byte, 2048)
			for {
				n, err = unix.Poll(poll_fds, -1) // -1 means wait indefinitely
				if err != nil {
					if errors.Is(err, unix.EINTR) { continue }
					c.log.Write("", LOG_ERROR, "[%s] Failed to poll pts stdout - %s", req.RemoteAddr, err.Error())
					break
				}
				if n == 0 { // timed out
					continue
				}

				if (poll_fds[0].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
					c.log.Write(pts.Id, LOG_DEBUG, "[%s] EOF detected on pts stdout", req.RemoteAddr)
					break;
				}

				if (poll_fds[0].Revents & unix.POLLIN) != 0 {
					n, err = out.Read(buf)
					if err != nil {
						if !errors.Is(err, io.EOF) {
							c.log.Write(pts.Id, LOG_ERROR, "[%s] Failed to read pts stdout - %s", req.RemoteAddr, err.Error())
						}
						break
					}
					if n > 0 {
						err = pts.send_ws_data(ws, "iov", string(buf[:n]))
						if err != nil {
							c.log.Write(pts.Id, LOG_ERROR, "[%s] Failed to send to websocket - %s", req.RemoteAddr, err.Error())
							break
						}
					}
				}
			}
			c.stats.pts_sessions.Add(-1)
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
								cmd, tty, err = pts.connect_pts(username, password)
								if err != nil {
									c.log.Write(pts.Id, LOG_ERROR, "[%s] Failed to connect pts - %s", req.RemoteAddr, err.Error())
									pts.send_ws_data(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error
								} else {
									err = pts.send_ws_data(ws, "status", "opened")
									if err != nil {
										c.log.Write(pts.Id, LOG_ERROR, "[%s] Failed to write opened event to websocket - %s", req.RemoteAddr, err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										c.log.Write(pts.Id, LOG_DEBUG, "[%s] Opened pts session", req.RemoteAddr)
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
							pty.Setsize(tty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
							c.log.Write(pts.Id, LOG_DEBUG, "[%s] Resized terminal to %d,%d", req.RemoteAddr, rows, cols)
							// ignore error
						}
				}
			}
		}
	}

	if tty != nil {
		err = pts.send_ws_data(ws, "status", "closed")
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
	c.log.Write(pts.Id, LOG_DEBUG, "[%s] Ended pts session", req.RemoteAddr)

	return http.StatusOK, err
}


// ------------------------------------------------------

func (pts *client_pts_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var err error

	c = pts.c

	switch pts.file {
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
						Mode: "pts",
						ConnId: "-1",
						RouteId: "-1",
					})
			}


		case "_forbidden":
			status_code = WriteEmptyRespHeader(w, http.StatusForbidden)

		case "_notfound":
			status_code = WriteEmptyRespHeader(w, http.StatusNotFound)

		default:
			if strings.HasPrefix(pts.file, "_redir:") {
				status_code = http.StatusMovedPermanently
				w.Header().Set("Location", pts.file[7:])
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
