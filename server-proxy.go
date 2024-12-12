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
import "text/template"
import "unsafe"

import "golang.org/x/crypto/ssh"
import "golang.org/x/net/http/httpguts"
import "golang.org/x/net/websocket"

const SERVER_PROXY_ID_COOKIE string = "hodu-proxy-id"
const SERVER_PROXY_MODE_COOKIE string = "hodu-proxy-mode"

const SERVER_PROXY_MODE_VERBATIM string = "verbatim"
const SERVER_PROXY_MODE_PREFIXED string = "prefixed"

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

func mutate_proxy_req_headers(req *http.Request, newreq *http.Request) {
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

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(conn_nid) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto oops
	}
	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(route_nid) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto oops
	}

	r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
	if r == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		goto done
	}

	w.Header().Add("Set-Cookie", fmt.Sprintf("%s=%s; Path=/; HttpOnly", SERVER_PROXY_MODE_COOKIE, SERVER_PROXY_MODE_VERBATIM))
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
	var conn_nid uint64
	var route_nid uint64
	var client *http.Client
	var resp *http.Response
	var tcp_conn *net.TCPConn
	var transport *http.Transport
	var addr net.TCPAddr
	var proxy_req *http.Request
	var proxy_url *url.URL
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
	mode, err = req.Cookie(SERVER_PROXY_MODE_COOKIE)
	if err == nil {
		if (mode.Value == SERVER_PROXY_MODE_PREFIXED) {
			// TODO:
		}
	}
	
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

	conn_nid, err = strconv.ParseUint(ids[0], 10, int(unsafe.Sizeof(conn_nid) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto oops
	}
	route_nid, err = strconv.ParseUint(ids[1], 10, int(unsafe.Sizeof(route_nid) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto oops
	}

	r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
	if r == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		goto done
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

	//proxy_url = fmt.Sprintf("%s://%s%s", proxy_proto, r.ptc_addr, req.URL.Path)
	proxy_url = &url.URL{
		Scheme:   proxy_proto,
		Host:     r.ptc_addr,
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
		Fragment: req.URL.Fragment,
	}

// TODO: http.NewRequestWithContext().??
	proxy_req, err = http.NewRequest(req.Method, proxy_url.String(), req.Body)
	if err != nil {
		status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
		goto oops
	}

	if httpguts.HeaderValuesContainsToken(req.Header["Connection"], "Upgrade") {
		req_upgrade_type = req.Header.Get("Upgrade")
	}
	mutate_proxy_req_headers(req, proxy_req)

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
	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(pxy.s.log, req, err) }
	}()
	
// TODO: logging
	switch pxy.file {
		case "xterm.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.WriteHeader(http.StatusOK)
			w.Write(xterm_js)
		case "xterm-addon-fit.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.WriteHeader(http.StatusOK)
			w.Write(xterm_addon_fit_js)
		case "xterm.css":
			w.Header().Set("Content-Type", "text/css")
			w.WriteHeader(http.StatusOK)
			w.Write(xterm_css)
		case "xterm.html":
			var tmpl *template.Template
			var err error

			tmpl = template.New("")
			_, err = tmpl.Parse(string(xterm_html))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				tmpl.Execute(w,
					&server_proxy_xterm_session_info{
						req.PathValue("conn_id"),
						req.PathValue("route_id"),
					})
			}
		case "_forbidden":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
	}
}
// ------------------------------------

type server_proxy_ssh_ws struct {
	s *Server
	h websocket.Handler
}

type json_ssh_ws_event struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// TODO: put this task to sync group.
// TODO: put the above proxy task to sync group too.


func server_proxy_serve_ssh_ws(ws *websocket.Conn, s *Server) {
	var req *http.Request
	var conn_id string
	var conn_nid uint64
	var route_id string
	var route_nid uint64
	var r *ServerRoute
	var addr net.TCPAddr
	var cc *ssh.ClientConfig
	var c *ssh.Client
	var sess *ssh.Session
	var in io.Writer
	var out io.Reader
	var err error

	req = ws.Request()

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(s.log, req, err) }
	}()

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(conn_nid) * 8))
	if err != nil {
		return
	}
	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(route_nid) * 8))
	if err != nil {
		return
	}

	r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
	if r == nil {
		// TODO: enhance logging. original request, conn_nid, route_nid
		s.log.Write("", LOG_ERROR, "No server route(%d,%d) found", conn_nid, route_nid)
		return
	}
	
	cc = &ssh.ClientConfig{
		 User: "hyung-hwan",
		Auth: []ssh.AuthMethod{
			ssh.Password("evianilie99"),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
	c, err = ssh.Dial("tcp", addr.String(), cc)
	if err != nil {
		s.log.Write("", LOG_ERROR, "SSH dial error - %s", err.Error())
		return
	}
	defer c.Close()

	sess, err = c.NewSession()
	if err != nil {
		s.log.Write("", LOG_ERROR, "SSH session error - %s", err.Error())
		return
	}
	defer sess.Close()

	out, err = sess.StdoutPipe()
	if err != nil {
		s.log.Write("", LOG_ERROR, "STDOUT pipe error - ", err.Error())
		return
	}

	in, err = sess.StdinPipe()
	if err != nil {
		s.log.Write("", LOG_ERROR, "STDIN pipe error - ", err.Error())
		return
	}

	err = sess.RequestPty("xterm", 40, 80, ssh.TerminalModes{})
	if err != nil {
		s.log.Write("", LOG_ERROR, "Request PTY error - ", err.Error())
		return
	}

	err = sess.Shell()
	if err != nil {
		s.log.Write("", LOG_ERROR, "Start shell error - ", err.Error())
		return
	}

	// Async reader and writer to websocket
	go func() {
		var buf []byte
		var n int
		var err error

		defer sess.Close()
		buf = make([]byte, 1024)
		for {
			n, err = out.Read(buf)
			if err != nil {
				if err != io.EOF {
					s.log.Write("", LOG_ERROR, "Read from SSH stdout error:", err)
				}
				return
			}
			if n > 0 {
				_, err = ws.Write(buf[:n])
				if err != nil {
					s.log.Write("", LOG_ERROR, "Write to WebSocket error:", err)
					return
				}
			}
		}
	}()

	// Sync websocket reader and writer to sshIn
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil {
			// TODO: check if EOF
			s.log.Write("", LOG_ERROR, "Failed to read from websocket - %s", err.Error())
			break
		}
		if len(msg) > 0 {
			var ev json_ssh_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "key":
						in.Write([]byte(ev.Data))
					case "resize":
						var sz []string
						sz = strings.Fields(ev.Data)
						if (len(sz) == 2) {
							var rows int
							var cols int
							rows, _ = strconv.Atoi(sz[0]);
							cols, _ = strconv.Atoi(sz[1]);
							sess.WindowChange(rows, cols)
							s.log.Write("", LOG_DEBUG, "Resized terminal to %d,%d", rows, cols)
							// ignore error
						}
				}
			}
		}
	}
}
