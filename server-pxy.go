package hodu

import "bufio"
import "context"
import "crypto/tls"
import "encoding/base64"
import "encoding/json"
import "errors"
import "fmt"
import "io"
import "net"
import "net/http"
import "net/netip"
import "net/url"
import "strconv"
import "strings"
import "sync"
import "text/template"
import "time"

import "golang.org/x/crypto/ssh"
import "golang.org/x/net/http/httpguts"
import "golang.org/x/net/websocket"

type server_pxy struct {
	S *Server
	Id string
}

type server_pxy_http_main struct {
	server_pxy
	prefix string
}

type server_pxy_xterm_file struct {
	server_pxy
	file string
}

type server_pxy_http_wpx struct {
	server_pxy
}

// this is minimal information for wpx to work
type ServerRouteProxyInfo struct {
	// command fields with ServerRoute
	SvcOption   RouteOption
	PtcAddr     string
	PtcName     string
	SvcAddr     *net.TCPAddr
	SvcPermNet  netip.Prefix

	// extra fields added after proessing
	PathPrefix  string
	ConnId      string
	RouteId     string
	IsForeign   bool
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

// ------------------------------------

func (pxy *server_pxy) Identity() string {
	return pxy.Id
}

func (pxy *server_pxy) Cors(req *http.Request) bool {
	return false
}

func (pxy *server_pxy) Authenticate(req *http.Request) (int, string) {
	return http.StatusOK, ""
}

// ------------------------------------

func prevent_follow_redirect(req *http.Request, via []*http.Request) error {
	return http.ErrUseLastResponse
}

func (pxy *server_pxy_http_main) get_route_proxy_info(req *http.Request, in_wpx_mode bool) (*ServerRouteProxyInfo, error) {
	var s *Server
	var conn_id string
	var route_id string
	var r *ServerRoute
	var pi *ServerRouteProxyInfo
	var path_prefix string
	var err error

	s = pxy.S

	if in_wpx_mode { // for wpx
		conn_id = req.PathValue("port_id")
		route_id = pxy.prefix // this is PORT_ID_MARKER
	} else {
		conn_id = req.PathValue("conn_id")
		route_id = req.PathValue("route_id")
	}
	if in_wpx_mode { // for wpx
		path_prefix = fmt.Sprintf("/%s", conn_id)
	} else {
		path_prefix = fmt.Sprintf("%s/%s/%s", pxy.prefix, conn_id, route_id)
	}

	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil {
		if !in_wpx_mode || s.wpx_foreign_port_proxy_maker == nil { return nil, err }

		// call this callback only in the wpx mode
		pi, err = s.wpx_foreign_port_proxy_maker("http", conn_id)
		if err != nil { return nil, err }
		pi.IsForeign = true // just to ensure this
	} else {
		pi = server_route_to_proxy_info(r)
		pi.IsForeign = false
	}
	pi.PathPrefix = path_prefix
	pi.ConnId = conn_id
	pi.RouteId = route_id

	return pi, nil
}

func (pxy *server_pxy_http_main) serve_upgraded(w http.ResponseWriter, req *http.Request, proxy_res *http.Response) error {
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

func (pxy *server_pxy_http_main) addr_to_transport(ctx context.Context, addr *net.TCPAddr) (*http.Transport, error) {
	var dialer *net.Dialer
	var waitctx context.Context
	var cancel_wait context.CancelFunc
	var conn net.Conn
	var tls_config *tls.Config
	var err error

	// establish the connection.
	dialer = &net.Dialer{}
	waitctx, cancel_wait = context.WithTimeout(ctx, 5 * time.Second) // TODO: make timeout configurable
	conn, err = dialer.DialContext(waitctx, TcpAddrClass(addr), addr.String())
	cancel_wait()
	if err != nil { return nil, err }

	if pxy.S.Cfg.PxyTargetTls != nil {
		tls_config = pxy.S.Cfg.PxyTargetTls.Clone()
	} else {
		tls_config = &tls.Config{InsecureSkipVerify: true}
	}
	// create a transport that uses the connection
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return conn, nil
		},
		TLSClientConfig: tls_config,
	}, nil
}

func (pxy *server_pxy_http_main) req_to_proxy_url(req *http.Request, r *ServerRouteProxyInfo) *url.URL {
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
	if r.PathPrefix != "" { proxy_url_path = strings.TrimPrefix(proxy_url_path, r.PathPrefix) }

	return &url.URL{
		Scheme:   proxy_proto,
		Host:     r.PtcAddr,
		Path:     proxy_url_path,
		RawQuery: req.URL.RawQuery,
		Fragment: req.URL.Fragment,
	}
}

func (pxy *server_pxy_http_main) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var pi *ServerRouteProxyInfo
	var status_code int
	var resp *http.Response
	var in_wpx_mode bool
	var transport *http.Transport
	var client *http.Client
	var addr *net.TCPAddr
	var proxy_req *http.Request
	var proxy_url *url.URL
	var upgrade_required bool
	var err error

	s = pxy.S
	in_wpx_mode = (pxy.prefix == PORT_ID_MARKER)

	pi, err = pxy.get_route_proxy_info(req, in_wpx_mode)
	if err != nil {
		status_code = WriteEmptyRespHeader(w, http.StatusNotFound)
		goto oops
	}

/*
	if pi.SvcOption & (RouteOption(ROUTE_OPTION_HTTP) | RouteOption(ROUTE_OPTION_HTTPS)) == 0 {
		status_code = WriteEmptyRespHeader(w, http.StatusForbidden)
		err = fmt.Errorf("target not http/https")
		goto oops
	}
*/
	addr = svc_addr_to_dst_addr(pi.SvcAddr)
	transport, err = pxy.addr_to_transport(s.Ctx, addr)
	if err != nil {
		status_code = WriteEmptyRespHeader(w, http.StatusBadGateway)
		goto oops
	}
	proxy_url = pxy.req_to_proxy_url(req, pi)

	s.log.Write(pxy.Id, LOG_INFO, "[%s] %s %s -> %+v", req.RemoteAddr, req.Method, req.RequestURI, proxy_url)

	proxy_req, err = http.NewRequestWithContext(s.Ctx, req.Method, proxy_url.String(), req.Body)
	if err != nil {
		status_code = WriteEmptyRespHeader(w, http.StatusInternalServerError)
		goto oops
	}
	upgrade_required = mutate_proxy_req_headers(req, proxy_req, pi.PathPrefix, in_wpx_mode)

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
		status_code = WriteEmptyRespHeader(w, http.StatusInternalServerError)
		goto oops
	} else {
		status_code = resp.StatusCode
		if upgrade_required && resp.StatusCode == http.StatusSwitchingProtocols {
			s.log.Write(pxy.Id, LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.RequestURI, status_code)
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
				resp_body = s.wpx_resp_tf(pi, resp)
			}

			outhdr = w.Header()
			copy_headers(outhdr, resp_hdr)
			delete_hop_by_hop_headers(outhdr)

			w.WriteHeader(status_code)

			_, err = io.Copy(w, resp_body)
			if err != nil {
				s.log.Write(pxy.Id, LOG_WARN, "[%s] %s %s %s", req.RemoteAddr, req.Method, req.RequestURI, err.Error())
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

func (pxy *server_pxy_http_wpx) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var status_code int
//	var err error

	status_code = WriteEmptyRespHeader(w, http.StatusForbidden)

// TODO: show the list of services running instead if enabled?

//done:
	return status_code, nil

//oops:
//	return status_code, err
}
// ------------------------------------

func (pxy *server_pxy_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var err error

	s = pxy.S

	switch pxy.file {
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
			var conn_id string
			var route_id string

			// this endpoint is registered for /_ssh/{conn_id}/{route_id}/ under pxy.
			// and for /_ssh/{port_id} under wpx.
			if pxy.Id == HS_ID_WPX {
				conn_id = req.PathValue("port_id")
				route_id = PORT_ID_MARKER
				_, err = s.FindServerRouteByIdStr(conn_id, route_id)
				if err != nil && s.wpx_foreign_port_proxy_maker != nil {
					_, err = s.wpx_foreign_port_proxy_maker("ssh", conn_id)
				}
			} else  {
				conn_id = req.PathValue("conn_id")
				route_id = req.PathValue("route_id")
				_, err = s.FindServerRouteByIdStr(conn_id, route_id)
			}
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusNotFound)
				goto oops
			}

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
						Mode: "ssh",
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
			status_code = WriteEmptyRespHeader(w, http.StatusForbidden)

		case "_notfound":
			status_code = WriteEmptyRespHeader(w, http.StatusNotFound)

		default:
			if strings.HasPrefix(pxy.file, "_redir:") {
				status_code = http.StatusMovedPermanently
				w.Header().Set("Location", pxy.file[7:])
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

// ------------------------------------
type server_pxy_ssh_ws struct {
	S *Server
	ws *websocket.Conn
	Id string
}

func (pxy *server_pxy_ssh_ws) Identity() string {
	return pxy.Id
}

// TODO: put this task to sync group.
// TODO: put the above proxy task to sync group too.

func (pxy *server_pxy_ssh_ws) connect_ssh(ctx context.Context, username string, password string, r *ServerRoute) (*ssh.Client, *ssh.Session, io.Writer, io.Reader, error) {
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

	// [NOTE]
	// There is no authentication implemented for this websocket endpoint
	// I suppose authentication should be done at the ssh layer.
	// However, this can open doors to DoS attacks.
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
	conn, err = dialer.DialContext(ctx, TcpAddrClass(addr), addr.String())
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

func (pxy *server_pxy_ssh_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var port_id string
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
	var connect_ssh_cancel Atom[context.CancelFunc]
	var err error

	s = pxy.S
	req = ws.Request()
	conn_ready_chan = make(chan bool, 3)

	port_id = req.PathValue("port_id")
	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	if port_id != "" && conn_id == "" && route_id == "" {
		// called using the wpx endpoint. pxy.Id must be HS_ID_WPX
		conn_id = port_id
		route_id = PORT_ID_MARKER
	}

	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil && route_id == PORT_ID_MARKER && s.wpx_foreign_port_proxy_maker != nil {
		var pi *ServerRouteProxyInfo
		pi, err = s.wpx_foreign_port_proxy_maker("ssh", conn_id)
		if err != nil {
			send_ws_data_for_xterm(ws, "error", err.Error())
			goto done
		}

		// [SUPER-IMPORTANT!!]
		// create a fake server route. this is not a compleete structure.
		// some pointer fields are nil. extra care needs to be taken
		// below to ensure it doesn't access undesired fields when exitending
		// code further
		r = proxy_info_to_server_route(pi)
	}
	if err != nil {
		send_ws_data_for_xterm(ws, "error", err.Error())
		goto done
	}

	wg.Add(1)
	go func() {
		var conn_ready bool

		defer wg.Done()
		defer ws.Close() // dirty way to break the main loop

		conn_ready = <-conn_ready_chan
		if conn_ready { // connected
			var buf [2048]byte
			var n int
			var err error

			s.stats.ssh_proxy_sessions.Add(1)
			for {
				n, err = out.Read(buf[:])
				if n > 0 {
					var err2 error
					err2 = send_ws_data_for_xterm(ws, "iov", base64.StdEncoding.EncodeToString(buf[:n]))
					if err2 != nil {
						s.log.Write(pxy.Id, LOG_ERROR, "[%s] Failed to send to websocket - %s", req.RemoteAddr, err2.Error())
						break
					}
				}
				if err != nil {
					if !errors.Is(err, io.EOF) {
						s.log.Write(pxy.Id, LOG_ERROR, "[%s] Failed to read from SSH stdout - %s", req.RemoteAddr, err.Error())
					}
					break
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
			var ev json_xterm_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if sess == nil && len(ev.Data) == 2 {
							var cancel context.CancelFunc

							username = string(ev.Data[0])
							password = string(ev.Data[1])

							connect_ssh_ctx, cancel = context.WithTimeout(req.Context(), 10 * time.Second) // TODO: configurable timeout
							connect_ssh_cancel.Set(cancel)

							wg.Add(1)
							go func() {
								var err error

								defer wg.Done()
								c, sess, in, out, err = pxy.connect_ssh(connect_ssh_ctx, username, password, r)
								if err != nil {
									s.log.Write(pxy.Id, LOG_ERROR, "[%s] Failed to connect ssh - %s", req.RemoteAddr, err.Error())
									send_ws_data_for_xterm(ws, "error", err.Error())
									ws.Close() // dirty way to flag out the error
								} else {
									err = send_ws_data_for_xterm(ws, "status", "opened")
									if err != nil {
										s.log.Write(pxy.Id, LOG_ERROR, "[%s] Failed to write opened event to websocket - %s", req.RemoteAddr, err.Error())
										ws.Close() // dirty way to flag out the error
									} else {
										s.log.Write(pxy.Id, LOG_DEBUG, "[%s] Opened SSH session", req.RemoteAddr)
										conn_ready_chan <- true
									}
								}
								(connect_ssh_cancel.Get())()
								connect_ssh_cancel.Set(nil) // @@@ use atomic
							}()
						}

					case "close":
						var cancel context.CancelFunc
						cancel = connect_ssh_cancel.Get() // is it a good way to avoid mutex against Set() marked with @@@ above?
						if cancel != nil { cancel() }
						break ws_recv_loop

					case "iov":
						if sess != nil {
							var i int
							for i, _ = range ev.Data {
								//in.Write([]byte(ev.Data[i]))
								var bytes []byte
								bytes, err = base64.StdEncoding.DecodeString(ev.Data[i])
								if err != nil {
									s.log.Write(pxy.Id, LOG_WARN, "[%s] Invalid pxy iov data received - %s", req.RemoteAddr, ev.Data[i])
								} else {
									in.Write(bytes)
								}
							}
						}

					case "size":
						if sess != nil && len(ev.Data) == 2 {
							var rows int
							var cols int
							rows, _ = strconv.Atoi(ev.Data[0])
							cols, _ = strconv.Atoi(ev.Data[1])
							sess.WindowChange(rows, cols)
							s.log.Write(pxy.Id, LOG_DEBUG, "[%s] Resized terminal to %d,%d", req.RemoteAddr, rows, cols)
							// ignore error
						}
				}
			}
		}
	}

	if sess != nil {
		err = send_ws_data_for_xterm(ws, "status", "closed")
		if err != nil { goto done }
	}

done:
	conn_ready_chan <- false
	ws.Close()
	if sess != nil { sess.Close() }
	if c != nil { c.Close() }
	wg.Wait()
	s.log.Write(pxy.Id, LOG_DEBUG, "[%s] Ended SSH Session", req.RemoteAddr)

	return http.StatusOK, err
}
