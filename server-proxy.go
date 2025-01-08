package hodu

import "bufio"
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

import "golang.org/x/crypto/ssh"
import "golang.org/x/net/http/httpguts"
import "golang.org/x/net/websocket"

//go:embed xterm.js
var xterm_js []byte
//go:embed xterm-addon-fit.js
var xterm_addon_fit_js []byte
//go:embed xterm.css
var xterm_css []byte
//go:embed xterm.html
var xterm_html string

type server_proxy struct {
	s *Server
	id string
}

type server_proxy_http_main struct {
	server_proxy
	prefix string
}

type server_proxy_xterm_file struct {
	server_proxy
	file string
}

type server_proxy_http_wpx struct {
	server_proxy
}

// ------------------------------------

//Copied from net/http/httputil/reverseproxy.go
var hop_headers = []string{
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

func copy_headers(dst http.Header, src http.Header) {
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

	for _, h = range hop_headers {
		header.Del(h)
	}
}

func mutate_proxy_req_headers(req *http.Request, newreq *http.Request, path_prefix string, in_wpx_mode bool) bool {
	var hdr http.Header
	var newhdr http.Header
	var remote_addr string
	var local_port string
	var oldv []string
	var ok bool
	var err error
	var conn_addr net.Addr
	var upgrade_required bool

	//newreq.Header = req.Header.Clone()
	hdr = req.Header
	newhdr = newreq.Header

	copy_headers(newhdr, hdr)
	delete_hop_by_hop_headers(newhdr)

	// put back the upgrade header removed by delete_hop_by_hop_headers
	if httpguts.HeaderValuesContainsToken(hdr["Connection"], "Upgrade") {
		newhdr.Set("Connection", "Upgrade")
		newhdr.Set("Upgrade", hdr.Get("Upgrade"))
		upgrade_required = true
	}

/*
	if httpguts.HeaderValuesContainsToken(hdr["Te"], "trailers") {
		newhdr.Set("Te", "trailers")
	}
*/

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

	if !in_wpx_mode && path_prefix != "" {
		var v []string

		_, ok = newhdr["X-Forwarded-Path"]
		if !ok {
			newhdr.Set("X-Forwarded-Path", req.URL.Path)
		}

		v, ok = newhdr["X-Forwarded-Prefix"]
		if !ok {
			newhdr.Set("X-Forwarded-Prefix", path_prefix)
		} else {
			// TODO: how to multiple existing items...
			//       there isn't supposed to be multiple items...
			newhdr.Set("X-Forwarded-Prefix", v[0] + path_prefix)
		}
	}

	return upgrade_required
}

func (pxy *server_proxy) GetId() string {
	return pxy.id
}

// ------------------------------------

func prevent_follow_redirect (req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

func (pxy *server_proxy_http_main) get_route(req *http.Request, in_wpx_mode bool) (*ServerRoute, string, string, string, error) {
	var conn_id string
	var route_id string
	var r *ServerRoute
	var path_prefix string
	var err error

	if in_wpx_mode { // for wpx
		conn_id = req.PathValue("port_id")
		route_id = pxy.prefix
	} else {
		conn_id = req.PathValue("conn_id")
		route_id = req.PathValue("route_id")
	}
	if in_wpx_mode { // for wpx
		path_prefix = fmt.Sprintf("/%s", conn_id)
	} else {
		path_prefix = fmt.Sprintf("%s/%s/%s", pxy.prefix, conn_id, route_id)
	}

	r, err = pxy.s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil {	return nil, "", "", "", err	}

	return r, path_prefix, conn_id, route_id, nil
}

func (pxy *server_proxy_http_main) serve_upgraded(w http.ResponseWriter, req *http.Request, proxy_res *http.Response) error {
	var err_chan chan error
	var proxy_res_body io.ReadWriteCloser
	var rc *http.ResponseController
	var client_conn net.Conn
	var buf_rw *bufio.ReadWriter
	var ok bool
	var err error

	proxy_res_body, ok = proxy_res.Body.(io.ReadWriteCloser)
	if !ok {
		return fmt.Errorf("internal error - unable to cast upgraded response body")
	}
	defer proxy_res_body.Close()

	rc = http.NewResponseController(w)
	client_conn, buf_rw, err = rc.Hijack() // take over the connection.
	if err != nil { return err }

	defer client_conn.Close()

	copy_headers(w.Header(), proxy_res.Header)
	proxy_res.Header = w.Header()

	// reset it to make Write() and Flush() to handle the headers only.
	// the goroutines below will use the saved proxy_res_body.
	proxy_res.Body = nil

	err = proxy_res.Write(buf_rw)
	if err != nil { return fmt.Errorf("unable to write upgraded response header - %s", err.Error()) }

	err = buf_rw.Flush()
	if err != nil { return fmt.Errorf("unable to flush upgraded response header - %s", err.Error()) }

	err_chan = make(chan error, 2)
	go func() {
		var err error
		_, err =  io.Copy(client_conn, proxy_res_body)
		err_chan <- err
	}()

	go func() {
		var err error
		_, err =  io.Copy(proxy_res_body, client_conn)
		err_chan <- err
	}()
	err =<-err_chan

	return err
}

func (pxy *server_proxy_http_main) addr_to_transport (ctx context.Context, addr *net.TCPAddr) (*http.Transport, error) {
	var dialer *net.Dialer
	var waitctx context.Context
	var cancel_wait context.CancelFunc
	var conn net.Conn
	var err error

	// establish the connection.
	dialer = &net.Dialer{}
	waitctx, cancel_wait = context.WithTimeout(ctx, 3 * time.Second) // TODO: make timeout configurable
	conn, err = dialer.DialContext(waitctx, "tcp", addr.String())
	cancel_wait()
	if err != nil { return nil, err }

	// create a transport that uses the connection
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return conn, nil
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // TODO: make this part configurable?
	}, nil
}

func (pxy *server_proxy_http_main) req_to_proxy_url (req *http.Request, r *ServerRoute, path_prefix string) *url.URL {
	var proxy_proto string
	var proxy_url_path string

	// HTTP or HTTPS is actually a hint to the client-side peer
	// Use the hint to compose the URL to the client via the server-side
	// listening socket as if it connects to the client-side peer
	if r.SvcOption & RouteOption(ROUTE_OPTION_HTTPS) != 0 {
		proxy_proto = "https"
	} else {
		proxy_proto = "http"
	}

	proxy_url_path = req.URL.Path
	if path_prefix != "" { proxy_url_path = strings.TrimPrefix(proxy_url_path, path_prefix) }

	return &url.URL{
		Scheme:   proxy_proto,
		Host:     r.PtcAddr,
		Path:     proxy_url_path,
		RawQuery: req.URL.RawQuery,
		Fragment: req.URL.Fragment,
	}
}

func (pxy *server_proxy_http_main) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var r *ServerRoute
	var status_code int
	var path_prefix string
	var resp *http.Response
	var in_wpx_mode bool
	var transport *http.Transport
	var client *http.Client
	var addr *net.TCPAddr
	var proxy_req *http.Request
	var proxy_url *url.URL
	var upgrade_required bool
	var err error

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(pxy.s.log, req, err) }
	}()

	s = pxy.s
	in_wpx_mode = pxy.prefix == PORT_ID_MARKER

	r, path_prefix, _, _, err = pxy.get_route(req, in_wpx_mode)
	if err != nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		goto oops
	}

/*
	if r.SvcOption & (RouteOption(ROUTE_OPTION_HTTP) | RouteOption(ROUTE_OPTION_HTTPS)) == 0 {
		status_code = http.StatusForbidden; w.WriteHeader(status_code)
		err = fmt.Errorf("target not http/https")
		goto oops
	}
*/
	addr = svc_addr_to_dst_addr(r.SvcAddr)
	transport, err = pxy.addr_to_transport(s.ctx, addr)
	if err != nil {
		status_code = http.StatusBadGateway; w.WriteHeader(status_code)
		goto oops
	}
	proxy_url = pxy.req_to_proxy_url(req, r, path_prefix)

	s.log.Write(pxy.id, LOG_INFO, "[%s] %s %s -> %+v", req.RemoteAddr, req.Method, req.URL.String(), proxy_url)

	proxy_req, err = http.NewRequestWithContext(s.ctx, req.Method, proxy_url.String(), req.Body)
	if err != nil {
		status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
		goto oops
	}
	upgrade_required = mutate_proxy_req_headers(req, proxy_req, path_prefix, in_wpx_mode)

	if in_wpx_mode {
		proxy_req.Header.Set("Accept-Encoding", "")
	}

	client = &http.Client{
		Transport: transport,
		CheckRedirect: prevent_follow_redirect,
		// don't specify Timeout for this here or make it configurable...
	}
	resp, err = client.Do(proxy_req)
	//resp, err = transport.RoundTrip(proxy_req) // any advantage if using RoundTrip instead?
	if err != nil {
		status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
		goto oops
	} else {
		status_code = resp.StatusCode
		if upgrade_required && resp.StatusCode == http.StatusSwitchingProtocols {
			s.log.Write(pxy.id, LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code)
			err = pxy.serve_upgraded(w, req, resp)
			if err != nil { goto oops }
			return 0, nil// print the log mesage before calling serve_upgraded() and exit here
		} else {
			var outhdr http.Header
			var resp_hdr http.Header
			var resp_body io.Reader

			defer resp.Body.Close()
			resp_hdr = resp.Header
			resp_body = resp.Body

			if in_wpx_mode && s.wpx_resp_tf != nil {
				resp_body = s.wpx_resp_tf(r, path_prefix, resp)
			}

			outhdr = w.Header()
			copy_headers(outhdr, resp_hdr)
			delete_hop_by_hop_headers(outhdr)

			w.WriteHeader(status_code)

			_, err = io.Copy(w, resp_body)
			if err != nil {
				s.log.Write(pxy.id, LOG_WARN, "[%s] %s %s %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
			}

			// TODO: handle trailers
		}
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (pxy *server_proxy_http_wpx) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var status_code int
	var err error


	status_code = http.StatusForbidden; w.WriteHeader(status_code)

// TODO: show the list of services running...
//done:
	return status_code, nil

//oops:
	return status_code, err
}
// ------------------------------------

type server_proxy_xterm_session_info struct {
	ConnId string
	RouteId string
}

func (pxy *server_proxy_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
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
			var conn_id string
			var route_id string

			conn_id = req.PathValue("conn_id")
			route_id = req.PathValue("route_id")
			_, err = s.FindServerRouteByIdStr(conn_id, route_id)
			if err != nil {
				status_code = http.StatusNotFound; w.WriteHeader(status_code)
				goto oops
			}

			tmpl = template.New("")
			if s.xterm_html !=  "" {
				_, err = tmpl.Parse(s.xterm_html)
			} else {
				_, err = tmpl.Parse(xterm_html)
			}
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
		case "_redirect":
			// shorthand for /_ssh/{conn_id}/_/
			// don't care about parameters following the path
			status_code = http.StatusMovedPermanently
			w.Header().Set("Location", req.URL.Path + "_/")
			w.WriteHeader(status_code)
		case "_forbidden":
			status_code = http.StatusForbidden; w.WriteHeader(status_code)
		default:
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

type server_proxy_ssh_ws struct {
	s *Server
	ws *websocket.Conn
	id string
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
	var addr *net.TCPAddr
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
		// Timeout: 5 * time.Second , // timeout is also set on the passed ctx. it may not been needed here.
	}

/* Is this protection needed?
	if r.SvcOption & RouteOption(ROUTE_OPTION_SSH) == 0 {
		err = fmt.Errorf("target not ssh")
		goto oops
	}
*/

	addr = svc_addr_to_dst_addr(r.SvcAddr)

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
	var conn_id string
	var route_id string
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
	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil {
		pxy.send_ws_data(ws, "error", err.Error())
		goto done
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
						s.log.Write(pxy.id, LOG_ERROR, "Read from SSH stdout error - %s", err.Error())
					}
					break
				}
				if n > 0 {
					err = pxy.send_ws_data(ws, "iov", string(buf[:n]))
					if err != nil {
						s.log.Write(pxy.id, LOG_ERROR, "Failed to send to websocket - %s", err.Error())
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
									s.log.Write(pxy.id, LOG_ERROR, "failed to connect ssh - %s", err.Error())
									pxy.send_ws_data(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error
								} else {
									err = pxy.send_ws_data(ws, "status", "opened")
									if err != nil {
										s.log.Write(pxy.id, LOG_ERROR, "Failed to write opened event to websocket - %s", err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										conn_ready_chan <- true
									}
								}
								connect_ssh_cancel()
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
							s.log.Write(pxy.id, LOG_DEBUG, "Resized terminal to %d,%d", rows, cols)
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
		s.log.Write(pxy.id, LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	} else {
		s.log.Write(pxy.id, LOG_DEBUG, "[%s] %s %s - ended", req.RemoteAddr, req.Method, req.URL.String())
	}
}
