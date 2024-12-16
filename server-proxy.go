package hodu

import "context"
import "crypto/tls"
import _ "embed"
import "encoding/json"
import "fmt"
import "io"
import "net"
import "net/http"
import "net/url"
import "strconv"
import "strings"
import "sync"
import "text/template"
import "time"
import "unsafe"

import "golang.org/x/crypto/ssh"
import "golang.org/x/net/http/httpguts"
import "golang.org/x/net/websocket"

const SERVER_PROXY_ID_COOKIE string = "hodu-proxy-id"

//go:embed xterm.js
var xterm_js []byte
//go:embed xterm-addon-fit.js
var xterm_addon_fit_js []byte
//go:embed xterm.css
var xterm_css []byte
//go:embed xterm.html
var xterm_html []byte

type server_proxy_http_init struct {
	s *Server
}

type server_proxy_http_main struct {
	s *Server
	prefix string
}

type server_proxy_ssh struct {
	s *Server
}
// ------------------------------------

//Copied from net/http/httputil/reverseproxy.go
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers", // not Trailers per URL above; https://www.rfc-editor.org/errata_search.php?eid=4522
	"Transfer-Encoding",
	"Upgrade",
}

func copy_headers(src http.Header, dst http.Header) {
	var key string
	var val string
	var vals []string

	for key, vals = range src {
		for _, val = range vals {
			dst.Add(key, val)
		}
	}
}

func delete_hop_by_hop_headers(header http.Header) {
	var h string

	for _, h = range hopHeaders {
		header.Del(h)
	}
}

func mutate_proxy_req_headers(req *http.Request, newreq *http.Request, add_path bool) {
	var hdr http.Header
	var newhdr http.Header
	var remote_addr string
	var local_port string
	var oldv []string
	var ok bool
	var err error
	var conn_addr net.Addr

	//newreq.Header = req.Header.Clone()
	copy_headers(req.Header, newreq.Header)
	delete_hop_by_hop_headers(newreq.Header)

	hdr = req.Header
	newhdr = newreq.Header
	remote_addr, _, err = net.SplitHostPort(req.RemoteAddr)
	if err == nil {
		oldv, ok = hdr["X-Forwarded-For"]
		if ok { remote_addr = strings.Join(oldv, ", ") + ", " + remote_addr }
		newhdr.Set("X-Forwarded-For", remote_addr)
	}

	conn_addr, ok = req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if ok {
		_, local_port, err = net.SplitHostPort(conn_addr.String())
		if err == nil {
			oldv, ok = newhdr["X-Forwarded-Port"]
			if !ok { newhdr.Set("X-Fowarded-Port", local_port) }
		}
	}

	_, ok = newhdr["X-Forwarded-Proto"]
	if !ok {
		var proto string
		if req.TLS == nil {
			proto = "http"
		} else {
			proto = "https"
		}
		newhdr.Set("X-Fowarded-Proto", proto)
	}

	_, ok = newhdr["X-Forwarded-Host"]
	if !ok {
		newhdr.Set("X-Forwarded-Host", req.Host)
	}

	if add_path {
		_, ok = newhdr["X-Forwarded-Path"]
		if !ok {
			newhdr.Set("X-Forwarded-Path", req.URL.Path)
		}
	}
}
// ------------------------------------

func (pxy *server_proxy_http_init) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var r *ServerRoute
	var status_code int
	var conn_id string
	var conn_nid uint64
	var route_id string
	var route_nid uint64
	var err error

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(pxy.s.log, req, err) }
	}()

	s = pxy.s

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto oops
	}
	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto oops
	}

	r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
	if r == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		goto done
	}

	w.Header().Add("Set-Cookie", fmt.Sprintf("%s=%d-%d; Path=/; HttpOnly", SERVER_PROXY_ID_COOKIE, conn_nid, route_nid)) // use the interpreted ids.
	w.Header().Set("Location", strings.TrimPrefix(req.URL.Path, fmt.Sprintf("/_init/%s/%s", conn_id, route_id))) // use the orignal id srings
	status_code = http.StatusFound; w.WriteHeader(status_code)

done:
	s.log.Write("", LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code)
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s %d - %s", req.RemoteAddr, req.Method, req.URL.String(), status_code, err.Error())
	return
}

// ------------------------------------

func prevent_follow_redirect (req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

func (pxy *server_proxy_http_main) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var r *ServerRoute
	var status_code int
	var mode *http.Cookie
	var id *http.Cookie
	var ids []string
	var conn_id string
	var route_id string
	var conn_nid uint64
	var route_nid uint64
	var prefixed bool
	var client *http.Client
	var resp *http.Response
	var tcp_conn *net.TCPConn
	var transport *http.Transport
	var addr net.TCPAddr
	var proxy_req *http.Request
	var proxy_url *url.URL
	var proxy_url_path string
	var proxy_proto string
	var req_upgrade_type string
	var err error

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(pxy.s.log, req, err) }
	}()

	s = pxy.s

/*
	ctx := req.Context()
	if ctx.Done() != nil {
	}
*/

	prefixed = true
	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	if conn_id == "" && route_id == "" {
		// it's not via  /_http/<<conn-id>>/<<route-id>>

		id, err = req.Cookie(SERVER_PROXY_ID_COOKIE)
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			goto oops
		}

		ids = strings.Split(id.Value, "-")
		if (len(ids) != 2) {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			err = fmt.Errorf("invalid proxy id cookie value - %s", id.Value)
			goto oops
		}

		prefixed = false
		conn_id = ids[0]
		route_id = ids[1]
	}

	if route_id == "_" {
		var port_nid uint64

		port_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			goto oops
		}

		r = s.FindServerRouteByPortId(PortId(port_nid))
		if r == nil {
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
			goto done
		}
	} else {
		conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			goto oops
		}
		route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			goto oops
		}

		r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
		if r == nil {
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
			goto done
		}
	}

	addr = *r.svc_addr;
	if addr.IP.To4() != nil {
		addr.IP = net.IPv4(127, 0, 0, 1) // net.IPv4loopback is not defined. so use net.IPv4()
	} else {
		addr.IP = net.IPv6loopback // net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	}
	tcp_conn, err = net.DialTCP("tcp", nil, &addr) // need to be specific between tcp4 and tcp6? maybe not
	if err != nil {
		status_code = http.StatusBadGateway; w.WriteHeader(status_code)
		goto oops
	}

	transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return tcp_conn, nil
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client = &http.Client{
		Transport: transport,
		CheckRedirect: prevent_follow_redirect,
	}

	// HTTP or HTTPS is actually a hint to the client-side peer
	// Use the hint to compose the URL to the client via the server-side
	// listening socket as if it connects to the client-side peer
	if r.svc_option & RouteOption(ROUTE_OPTION_HTTPS) != 0 {
		proxy_proto = "https"
	} else {
		proxy_proto = "http"
	}

	proxy_url_path = req.URL.Path
	if prefixed {
		proxy_url_path = strings.TrimPrefix(proxy_url_path, fmt.Sprintf("%s/%s/%s", pxy.prefix, conn_id, route_id))
	}

	//proxy_url = fmt.Sprintf("%s://%s%s", proxy_proto, r.ptc_addr, req.URL.Path)
	proxy_url = &url.URL{
		Scheme:   proxy_proto,
		Host:     r.ptc_addr,
		Path:     proxy_url_path,
		RawQuery: req.URL.RawQuery,
		Fragment: req.URL.Fragment,
	}

	s.log.Write("", LOG_DEBUG, "[%s] %s %s -> %+v", req.RemoteAddr, req.Method, req.URL.String(), proxy_url)

// TODO: http.NewRequestWithContext().??
	proxy_req, err = http.NewRequest(req.Method, proxy_url.String(), req.Body)
	if err != nil {
		status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
		goto oops
	}

	if httpguts.HeaderValuesContainsToken(req.Header["Connection"], "Upgrade") {
		req_upgrade_type = req.Header.Get("Upgrade")
	}
	mutate_proxy_req_headers(req, proxy_req, prefixed)

	if httpguts.HeaderValuesContainsToken(req.Header["Te"], "trailers") {
		proxy_req.Header.Set("Te", "trailers")
	}
	if req_upgrade_type != "" {
		proxy_req.Header.Set("Connection", "Upgrade")
		proxy_req.Header.Set("Upgrade", req_upgrade_type)
	}

//fmt.Printf ("proxy NEW req [%+v]\n", proxy_req)

	resp, err = client.Do(proxy_req)
	if err != nil {
		status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
		goto oops
	} else {
		var hdr http.Header
		//var loc string
		
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusSwitchingProtocols {
			// TODO:
		}

		hdr = w.Header()
		copy_headers(resp.Header, hdr)
		delete_hop_by_hop_headers(hdr)
		/*
		loc = hdr.Get("Location")
		if loc != "" {
			strings.Replace(lv, r.ptc_addr, req.Host
			hdr.Set("Location", xxx)
		}*/

		w.Header().Add("Set-Cookie", fmt.Sprintf("%s=%d-%d; Path=/; HttpOnly", SERVER_PROXY_ID_COOKIE, conn_nid, route_nid))
		status_code = resp.StatusCode; w.WriteHeader(status_code)
		io.Copy(w, resp.Body)

		// TODO: handle trailers
	}

done:
	s.log.Write("", LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code)
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s %d - %s", req.RemoteAddr, req.Method, req.URL.String(), status_code, err.Error())
	return
}


// ------------------------------------
type server_proxy_xterm_file struct {
	s *Server
	file string
}

type server_proxy_xterm_session_info struct {
	ConnId string
	RouteId string
}

func (pxy *server_proxy_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var status_code int
	var err error

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(pxy.s.log, req, err) }
	}()

	s = pxy.s

// TODO: logging
	switch pxy.file {
		case "xterm.js":
			w.Header().Set("Content-Type", "text/javascript")
			status_code = http.StatusOK; w.WriteHeader(status_code)
			w.Write(xterm_js)
		case "xterm-addon-fit.js":
			w.Header().Set("Content-Type", "text/javascript")
			status_code = http.StatusOK; w.WriteHeader(status_code)
			w.Write(xterm_addon_fit_js)
		case "xterm.css":
			w.Header().Set("Content-Type", "text/css")
			status_code = http.StatusOK; w.WriteHeader(status_code)
			w.Write(xterm_css)
		case "xterm.html":
			var tmpl *template.Template
			var port_id string
			var conn_id string
			var route_id string
			var port_nid uint64
			var conn_nid uint64
			var route_nid uint64
			var r *ServerRoute

			conn_id = req.PathValue("conn_id")
			route_id = req.PathValue("route_id")
			if route_id == "_" { port_id = conn_id }
		
			if port_id != "" {
				port_nid, err = strconv.ParseUint(port_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
				if err != nil {
					status_code = http.StatusBadRequest; w.WriteHeader(status_code)
					goto oops
				}
		
				r = s.FindServerRouteByPortId(PortId(port_nid))
				if r == nil {
					status_code = http.StatusNotFound; w.WriteHeader(status_code)
					goto done
				}
			} else {
				conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
				if err != nil {
					status_code = http.StatusBadRequest; w.WriteHeader(status_code)
					goto oops
				}
				route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
				if err != nil {
					status_code = http.StatusBadRequest; w.WriteHeader(status_code)
					goto oops
				}
		
				r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
				if r == nil {
					status_code = http.StatusNotFound; w.WriteHeader(status_code)
					goto done
				}
			}
		
			tmpl = template.New("")
			_, err = tmpl.Parse(string(xterm_html))
			if err != nil {
				status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
				goto oops
			} else {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				tmpl.Execute(w,
					&server_proxy_xterm_session_info{
						ConnId: conn_id,
						RouteId: route_id,
					})
			}
		case "_forbidden":
			status_code = http.StatusForbidden; w.WriteHeader(status_code)
		default:
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
	}


done:
	s.log.Write("", LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code)
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s %d - %s", req.RemoteAddr, req.Method, req.URL.String(), status_code, err.Error())
	return
}

// ------------------------------------

type server_proxy_ssh_ws struct {
	s *Server
	ws *websocket.Conn
}

type json_ssh_ws_event struct {
	Type string `json:"type"`
	Data []string `json:"data"`
}

// TODO: put this task to sync group.
// TODO: put the above proxy task to sync group too.

func (pxy *server_proxy_ssh_ws) send_ws_data(ws *websocket.Conn, type_val string, data string) error {
	var msg []byte
	var err error

	msg, err = json.Marshal(json_ssh_ws_event{Type: type_val, Data: []string{ data } })
	if err == nil { err = websocket.Message.Send(ws, msg) }
	return err
}

func (pxy *server_proxy_ssh_ws) connect_ssh (ctx context.Context, username string, password string, r *ServerRoute) ( *ssh.Client, *ssh.Session, io.Writer, io.Reader, error) {
	var cc *ssh.ClientConfig
	var addr net.TCPAddr
	var dialer *net.Dialer
	var conn net.Conn
	var ssh_conn ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	var c *ssh.Client
	var sess *ssh.Session
	var in io.Writer // input to target
	var out io.Reader // ooutput from target
	var err error

	cc = &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{ ssh.Password(password) },
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		// Timeout: 2 * time.Second ,
	}

// CHECK OPTIONS
	// if r.svc_option & RouteOption(ROUTE_OPTION_SSH) == 0 {
	// REJECT??
	//}
// TODO: timeout...

	addr = *r.svc_addr;
	if addr.IP.To4() != nil {
		addr.IP = net.IPv4(127, 0, 0, 1) // net.IPv4loopback is not defined. so use net.IPv4()
	} else {
		addr.IP = net.IPv6loopback // net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	}

	dialer = &net.Dialer{}
	conn, err = dialer.DialContext(ctx, "tcp", addr.String())
	if err != nil { goto oops }

	ssh_conn, chans, reqs, err = ssh.NewClientConn(conn, addr.String(), cc)
	if err != nil { goto oops }

	c = ssh.NewClient(ssh_conn, chans, reqs)

	sess, err = c.NewSession()
	if err != nil { goto oops }

	out, err = sess.StdoutPipe()
	if err != nil { goto oops }

	in, err = sess.StdinPipe()
	if err != nil { goto oops }

	err = sess.RequestPty("xterm", 25, 80, ssh.TerminalModes{})
	if err != nil { goto oops }

	err = sess.Shell()
	if err != nil { goto oops }

	return c, sess, in, out, nil

oops:
	if sess != nil { sess.Close() }
	if c != nil { c.Close() }
	return nil, nil, nil, nil, err
}

func (pxy *server_proxy_ssh_ws) ServeWebsocket(ws *websocket.Conn) {
	var s *Server
	var req *http.Request
	var port_id string
	var conn_id string
	var route_id string
	var port_nid uint64
	var conn_nid uint64
	var route_nid uint64
	var r *ServerRoute
	var username string
	var password string
	var c *ssh.Client
	var sess *ssh.Session
	var in io.Writer
	var out io.Reader
	var wg sync.WaitGroup
	var conn_ready_chan chan bool
	var connect_ssh_ctx context.Context
	var connect_ssh_cancel context.CancelFunc
	var err error

	s = pxy.s
	req = ws.Request()
	conn_ready_chan = make(chan bool, 3)

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(s.log, req, err) }
	}()

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	if route_id == "_" { port_id = conn_id }

	if port_id != "" {
		port_nid, err = strconv.ParseUint(port_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
		if err != nil {
			err = fmt.Errorf("invalid port id - %s", port_id)
			pxy.send_ws_data(ws, "error", err.Error())
			goto done
		}

		r = s.FindServerRouteByPortId(PortId(port_nid))
		if r == nil {
			err = fmt.Errorf("port(%d) not found", conn_nid, route_nid)
			pxy.send_ws_data(ws, "error", err.Error())
			goto done
		}
	} else {
		conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
		if err != nil {
			err = fmt.Errorf("invalid connection id - %s", conn_id)
			pxy.send_ws_data(ws, "error", err.Error())
			goto done
		}
		route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
		if err != nil {
			err = fmt.Errorf("invalid route id - %s", route_id)
			pxy.send_ws_data(ws, "error", err.Error())
			goto done
		}

		r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
		if r == nil {
			err = fmt.Errorf("route(%d,%d) not found", conn_nid, route_nid)
			pxy.send_ws_data(ws, "error", err.Error())
			goto done
		}
	}

	wg.Add(1)
	go func() {
		var conn_ready bool

		defer wg.Done()
		defer ws.Close() // dirty way to break the main loop

		conn_ready = <-conn_ready_chan
		if conn_ready { // connected
			var buf []byte
			var n int
			var err error

			s.stats.ssh_proxy_sessions.Add(1)
			buf = make([]byte, 2048)
			for {
				n, err = out.Read(buf)
				if err != nil {
					if err != io.EOF {
						s.log.Write("", LOG_ERROR, "Read from SSH stdout error - %s", err.Error())
					}
					break
				}
				if n > 0 {
					err = pxy.send_ws_data(ws, "iov", string(buf[:n]))
					if err != nil {
						s.log.Write("", LOG_ERROR, "Failed to send to websocket - %s", err.Error())
						break
					}
				}
			}
			s.stats.ssh_proxy_sessions.Add(-1)
		}
	}()

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil { goto done }

		if len(msg) > 0 {
			var ev json_ssh_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if sess == nil && len(ev.Data) == 2 {
							username = string(ev.Data[0])
							password = string(ev.Data[1])

							connect_ssh_ctx, connect_ssh_cancel = context.WithTimeout(req.Context(), 10 * time.Second) // TODO: configurable timeout

							wg.Add(1)
							go func() {
								var err error

								defer wg.Done()
								c, sess, in, out, err = pxy.connect_ssh(connect_ssh_ctx, username, password, r)
								if err != nil {
									s.log.Write("", LOG_ERROR, "failed to connect ssh - %s", err.Error())
									pxy.send_ws_data(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error
								} else {
									err = pxy.send_ws_data(ws, "status", "opened")
									if err != nil {
										s.log.Write("", LOG_ERROR, "Failed to write opened event to websocket - %s", err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										conn_ready_chan <- true
									}
								}
								connect_ssh_cancel = nil
							}()
						}

					case "close":
						var cancel context.CancelFunc
						cancel = connect_ssh_cancel // is it a good way to avoid mutex?
						if cancel != nil { cancel() }
						break ws_recv_loop

					case "iov":
						if sess != nil {
							var i int
							for i, _ = range ev.Data {
								in.Write([]byte(ev.Data[i]))
							}
						}

					case "size":
						if sess != nil && len(ev.Data) == 2 {
							var rows int
							var cols int
							rows, _ = strconv.Atoi(ev.Data[0])
							cols, _ = strconv.Atoi(ev.Data[1])
							sess.WindowChange(rows, cols)
							s.log.Write("", LOG_DEBUG, "Resized terminal to %d,%d", rows, cols)
							// ignore error
						}
				}
			}
		}
	}

	if sess != nil {
		err = pxy.send_ws_data(ws, "status", "closed")
		if err != nil { goto done }
	}

done:
	conn_ready_chan <- false
	ws.Close()
	if sess != nil { sess.Close() }
	if c != nil { c.Close() }
	wg.Wait()
	if err != nil {
		s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	} else {
		s.log.Write("", LOG_DEBUG, "[%s] %s %s - ended", req.RemoteAddr, req.Method, req.URL.String())
	}
}
