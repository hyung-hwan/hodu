package hodu

import "container/list"
import "encoding/json"
import "fmt"
import "net/http"
import "net/url"
import "strconv"
import "strings"
import "sync"
import "time"

import "golang.org/x/net/websocket"

type ServerTokenClaim struct {
	ExpiresAt int64 `json:"exp"`
	IssuedAt int64 `json:"iat"`
}

type json_out_token struct {
	AccessToken string `json:"access-token"`
	RefreshToken string `json:"refresh-token,omitempty"`
}

type json_out_server_conn struct {
	CId ConnId `json:"conn-id"`
	ServerAddr string `json:"server-addr"`
	ClientAddr string `json:"client-addr"`
	ClientToken string `json:"client-token"`
	CreatedMilli int64 `json:"created-milli"`
	Routes []json_out_server_route `json:"routes,omitempty"`
}

type json_out_server_route struct {
	CId ConnId `json:"conn-id"`
	RId RouteId `json:"route-id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientPeerName string `json:"client-peer-name"`
	ServerPeerOption string `json:"server-peer-option"`
	ServerPeerSvcAddr string `json:"server-peer-svc-addr"` // actual listening address
	ServerPeerSvcNet string `json:"server-peer-svc-net"`
	CreatedMilli int64 `json:"created-milli"`
}

type json_out_server_peer struct {
	CId ConnId `json:"conn-id"`
	RId RouteId `json:"route-id"`
	PId PeerId `json:"peer-id"`
	ServerPeerAddr string `json:"server-peer-addr"`
	ServerLocalAddr string `json:"server-local-addr"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientLocalAddr string `json:"client-local-addr"`
	CreatedMilli int64 `json:"created-milli"`
}

type json_out_server_conn_id struct {
	CId ConnId `json:"conn-id"`
}

type json_out_server_route_id struct {
	CId ConnId `json:"conn-id"`
	RId RouteId `json:"route-id"`
}

type json_out_server_peer_id struct {
	CId ConnId `json:"conn-id"`
	RId RouteId `json:"route-id"`
	PId PeerId `json:"peer-id"`
}

type json_out_server_stats struct {
	json_out_go_stats

	ServerConns int64 `json:"server-conns"`
	ServerRoutes int64 `json:"server-routes"`
	ServerPeers int64 `json:"server-peers"`

	SshProxySessions int64 `json:"pxy-ssh-sessions"`
     ServerPtsSessions int64 `json:"server-pts-sessions"`
}

// this is a more specialized variant of json_in_notice
type json_in_server_notice struct {
	Client string `json:"client"`
	Text string `json:"text"`
}

// ------------------------------------

type ServerCtl struct {
	S *Server
	Id string
	NoAuth bool // override the auth configuration if true
}

type server_ctl_token struct {
	ServerCtl
}

type server_ctl_server_conns struct {
	ServerCtl
}

type server_ctl_server_conns_id struct {
	ServerCtl
}

type server_ctl_server_conns_id_routes struct {
	ServerCtl
}

type server_ctl_server_conns_id_routes_id struct {
	ServerCtl
}

type server_ctl_server_conns_id_routes_id_peers struct {
	ServerCtl
}

type server_ctl_server_conns_id_routes_id_peers_id struct {
	ServerCtl
}

type server_ctl_server_conns_id_peers struct {
	ServerCtl
}

type server_ctl_server_routes struct {
	ServerCtl
}

type server_ctl_server_peers struct {
	ServerCtl
}

type server_ctl_notices struct {
	ServerCtl
}

type server_ctl_notices_id struct {
	ServerCtl
}

type server_ctl_stats struct {
	ServerCtl
}

type server_ctl_ws struct {
	ServerCtl
}

// ------------------------------------

func (ctl *ServerCtl) Identity() string {
	return ctl.Id
}

func (ctl *ServerCtl) Cors(req *http.Request) bool {
	return ctl.S.Cfg.CtlCors
}

func (ctl *ServerCtl) Authenticate(req *http.Request) (int, string) {
	if ctl.NoAuth || ctl.S.Cfg.CtlAuth == nil { return http.StatusOK, "" }
	return ctl.S.Cfg.CtlAuth.Authenticate(req)
}

// ------------------------------------

func (ctl *server_ctl_token) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var jwt *JWT[ServerTokenClaim]
			var claim ServerTokenClaim
			var tok string
			var now time.Time

			if s.Cfg.CtlAuth == nil || !s.Cfg.CtlAuth.Enabled || s.Cfg.CtlAuth.TokenRsaKey == nil {
				status_code = WriteJsonRespHeader(w, http.StatusForbidden)
				err = fmt.Errorf("auth not enabled or token rsa key not set")
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			now = time.Now()
			claim.IssuedAt = now.Unix()
			claim.ExpiresAt = now.Add(s.Cfg.CtlAuth.TokenTtl).Unix()
			jwt = NewJWT(s.Cfg.CtlAuth.TokenRsaKey, &claim)
			tok, err = jwt.SignRS512()
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			err = je.Encode(json_out_token{ AccessToken: tok }) // TODO: refresh token
			if err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var q url.Values
	var status_code int
	var je *json.Encoder
	var routes bool
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	q = req.URL.Query()
	routes, err = strconv.ParseBool(strings.ToLower(q.Get("routes")))
	if err != nil { routes = false }

	switch req.Method {
		case http.MethodGet:
			var js []json_out_server_conn
			var ci ConnId

			js = make([]json_out_server_conn, 0)
			s.cts_mtx.Lock()
			for _, ci = range s.cts_map.get_sorted_keys() {
				var cts *ServerConn
				var jsp []json_out_server_route
				var ri RouteId

				cts = s.cts_map[ci]
				if routes {
					jsp = make([]json_out_server_route, 0)
					cts.route_mtx.Lock()
					for _, ri = range cts.route_map.get_sorted_keys() {
						var r *ServerRoute
						r = cts.route_map[ri]
						jsp = append(jsp, json_out_server_route{
							CId: cts.Id,
							RId: r.Id,
							ClientPeerAddr: r.PtcAddr,
							ClientPeerName: r.PtcName,
							ServerPeerSvcAddr: r.SvcAddr.String(),
							ServerPeerSvcNet: r.SvcPermNet.String(),
							ServerPeerOption: r.SvcOption.String(),
							CreatedMilli: r.Created.UnixMilli(),
						})
					}
					cts.route_mtx.Unlock()
				}

				js = append(js, json_out_server_conn{
					CId: cts.Id,
					ClientAddr: cts.RemoteAddr.String(),
					ServerAddr: cts.LocalAddr.String(),
					ClientToken: cts.ClientToken.Get(),
					CreatedMilli: cts.Created.UnixMilli(),
					Routes: jsp,
				})
			}
			s.cts_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			s.ReqStopAllServerConns()
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var q url.Values
	var status_code int
	var je *json.Encoder
	var conn_id string
	var cts *ServerConn
	var routes bool
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	q = req.URL.Query()
	routes, err = strconv.ParseBool(strings.ToLower(q.Get("routes")))
	if err != nil { routes = false }

	conn_id = req.PathValue("conn_id")
	cts, err = s.FindServerConnByIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}


	switch req.Method {
		case http.MethodGet:
			var jsp []json_out_server_route
			var js *json_out_server_conn
			var ri RouteId

			if routes {
				jsp = make([]json_out_server_route, 0)
				cts.route_mtx.Lock()
				for _, ri = range cts.route_map.get_sorted_keys() {
					var r *ServerRoute

					r = cts.route_map[ri]
					jsp = append(jsp, json_out_server_route{
						CId: cts.Id,
						RId: r.Id,
						ClientPeerAddr: r.PtcAddr,
						ClientPeerName: r.PtcName,
						ServerPeerSvcAddr: r.SvcAddr.String(),
						ServerPeerSvcNet: r.SvcPermNet.String(),
						ServerPeerOption: r.SvcOption.String(),
						CreatedMilli: r.Created.UnixMilli(),
					})
				}
				cts.route_mtx.Unlock()
			}
			js = &json_out_server_conn{
				CId: cts.Id,
				ClientAddr: cts.RemoteAddr.String(),
				ServerAddr: cts.LocalAddr.String(),
				ClientToken: cts.ClientToken.Get(),
				CreatedMilli: cts.Created.UnixMilli(),
				Routes: jsp,
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			//s.RemoveServerConn(cts)
			cts.ReqStop()
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id_routes) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var err error
	var conn_id string
	var je *json.Encoder
	var cts *ServerConn

	s = ctl.S
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	cts, err = s.FindServerConnByIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jsp []json_out_server_route
			var ri RouteId

			jsp = make([]json_out_server_route, 0)
			cts.route_mtx.Lock()
			for _, ri = range cts.route_map.get_sorted_keys() {
				var r *ServerRoute

				r = cts.route_map[ri]
				jsp = append(jsp, json_out_server_route{
					CId: cts.Id,
					RId: r.Id,
					ClientPeerAddr: r.PtcAddr,
					ClientPeerName: r.PtcName,
					ServerPeerSvcAddr: r.SvcAddr.String(),
					ServerPeerSvcNet: r.SvcPermNet.String(),
					ServerPeerOption: r.SvcOption.String(),
					CreatedMilli: r.Created.UnixMilli(),
				})
			}
			cts.route_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jsp); err != nil { goto oops }

		case http.MethodDelete:
			// direct removal causes quite some clean-up issues.
			// make stop request to all server routes and let the task stop by themselves
			//cts.RemoveAllServerRoutes()
			cts.ReqStopAllServerRoutes()
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id_routes_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var conn_id string
	var route_id string
	var je *json.Encoder
	var r *ServerRoute
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	if ctl.Id == HS_ID_WPX && req.Method != http.MethodGet {
		// support the get method only, if invoked via the wpx endpoint
		status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
		goto done
	}

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil {
		/*
		if route_id == PORT_ID_MARKER && ctl.S.wpx_foreign_port_proxy_marker != nil {
			// don't care if the ctl call is from wpx or not. if the request
			// is by the port number(noted by route being PORT_ID_MARKER),
			// check if it's a foreign port
			var pi *ServerRouteProxyInfo
			// currenly, this is invoked via wpx only for ssh from xterm.html
			// ugly, but hard-code the type to "ssh" here for now...
			pi, err = ctl.S.wpx_foreign_port_proxy_maker("ssh", conn_id)
			if err == nil { r = proxy_info_to_server_route(pi) } // fake route
		}
		*/

		if ctl.Id == HS_ID_WPX && route_id == PORT_ID_MARKER && ctl.S.wpx_foreign_port_proxy_maker != nil {
			var pi *ServerRouteProxyInfo
			// currenly, this is invoked via wpx only for ssh from xterm.html
			// ugly, but hard-code the type to "ssh" here for now...
			pi, err = ctl.S.wpx_foreign_port_proxy_maker("ssh", conn_id)
			if err == nil { r = proxy_info_to_server_route(pi) } // fake route
		}
	}

	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var cts_id ConnId
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			// proxy_info_to_server_route() created the fake route but the function
			// doesn't fake the Cts field and leaves it to nil.
			if r.Cts == nil { cts_id = 0 } else { cts_id = r.Cts.Id }
			err = je.Encode(json_out_server_route{
				CId: cts_id,
				RId: r.Id,
				ClientPeerAddr: r.PtcAddr,
				ClientPeerName: r.PtcName,
				ServerPeerSvcAddr: r.SvcAddr.String(),
				ServerPeerSvcNet: r.SvcPermNet.String(),
				ServerPeerOption: r.SvcOption.String(),
				CreatedMilli: r.Created.UnixMilli(),
			})
			if err != nil { goto oops }

		case http.MethodDelete:
			/*if r is foreign {
				// foreign route
				ctl.S.wpx_foreign_port_proxy_stopper(conn_id)
			} else {*/
				// native route
				r.ReqStop()
			//}
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id_routes_id_peers) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var conn_id string
	var route_id string
	var je *json.Encoder
	var r *ServerRoute
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jcp []json_out_server_peer
			var pi PeerId

			jcp = make([]json_out_server_peer, 0)
			r.pts_mtx.Lock()
			for _, pi = range r.pts_map.get_sorted_keys() {
				var p *ServerPeerConn
				p = r.pts_map[pi]
				jcp = append(jcp, json_out_server_peer{
					CId: r.Cts.Id,
					RId: r.Id,
					PId: p.conn_id,
					ServerPeerAddr: p.conn.RemoteAddr().String(),
					ServerLocalAddr: p.conn.LocalAddr().String(),
					ClientPeerAddr: p.client_peer_raddr.Get(),
					ClientLocalAddr: p.client_peer_laddr.Get(),
					CreatedMilli: p.Created.UnixMilli(),
				})
			}
			r.pts_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jcp); err != nil { goto oops }

		case http.MethodDelete:
			r.ReqStopAllServerPeerConns()
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id_routes_id_peers_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var conn_id string
	var route_id string
	var peer_id string
	var je *json.Encoder
	var p *ServerPeerConn
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	peer_id = req.PathValue("peer_id")
	p, err = s.FindServerPeerConnByIdStr(conn_id, route_id, peer_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jcp *json_out_server_peer

			jcp = &json_out_server_peer{
				CId: p.route.Cts.Id,
				RId: p.route.Id,
				PId: p.conn_id,
				ServerPeerAddr: p.conn.RemoteAddr().String(),
				ServerLocalAddr: p.conn.LocalAddr().String(),
				ClientPeerAddr: p.client_peer_raddr.Get(),
				ClientLocalAddr: p.client_peer_laddr.Get(),
				CreatedMilli: p.Created.UnixMilli(),
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jcp); err != nil { goto oops }

		case http.MethodDelete:
			p.ReqStop()
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}
// ------------------------------------

func (ctl *server_ctl_server_conns_id_peers) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var err error
	var conn_id string
	var je *json.Encoder
	var cts *ServerConn

	s = ctl.S
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	cts, err = s.FindServerConnByIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jsp []json_out_server_peer
			var e *list.Element

			jsp = make([]json_out_server_peer, 0)
			cts.pts_mtx.Lock()
			for e = cts.pts_list.Front(); e != nil; e = e.Next() {
				var pts *ServerPeerConn
				pts = e.Value.(*ServerPeerConn)
				jsp = append(jsp, json_out_server_peer{
					CId: pts.route.Cts.Id,
					RId: pts.route.Id,
					PId: pts.conn_id,
					ServerPeerAddr: pts.conn.RemoteAddr().String(),
					ServerLocalAddr: pts.conn.LocalAddr().String(),
					ClientPeerAddr: pts.client_peer_raddr.Get(),
					ClientLocalAddr: pts.client_peer_laddr.Get(),
					CreatedMilli: pts.Created.UnixMilli(),
				})
			}
			cts.pts_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jsp); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_server_routes) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var js []json_out_server_route
			var e *list.Element

			js = make([]json_out_server_route, 0)
			s.route_mtx.Lock()
			for e = s.route_list.Front(); e != nil; e = e.Next() {
				var r *ServerRoute
				r = e.Value.(*ServerRoute)
				js = append(js, json_out_server_route{
					CId: r.Cts.Id,
					RId: r.Id,
					ClientPeerAddr: r.PtcAddr,
					ClientPeerName: r.PtcName,
					ServerPeerSvcAddr: r.SvcAddr.String(),
					ServerPeerSvcNet: r.SvcPermNet.String(),
					ServerPeerOption: r.SvcOption.String(),
					CreatedMilli: r.Created.UnixMilli(),
				})
			}
			s.route_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(js); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------
func (ctl *server_ctl_server_peers) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var js []json_out_server_peer
			var e *list.Element

			js = make([]json_out_server_peer, 0)
			s.pts_mtx.Lock()
			for e = s.pts_list.Front(); e != nil; e = e.Next() {
				var pts *ServerPeerConn
				pts = e.Value.(*ServerPeerConn)
				js = append(js, json_out_server_peer{
					CId: pts.route.Cts.Id,
					RId: pts.route.Id,
					PId: pts.conn_id,
					ServerPeerAddr: pts.conn.RemoteAddr().String(),
					ServerLocalAddr: pts.conn.LocalAddr().String(),
					ClientPeerAddr: pts.client_peer_raddr.Get(),
					ClientLocalAddr: pts.client_peer_laddr.Get(),
					CreatedMilli: pts.Created.UnixMilli(),
				})
			}
			s.pts_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(js); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_notices) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodPost:
			var noti json_in_server_notice

			err = json.NewDecoder(req.Body).Decode(&noti)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			err = s.SendNotice(noti.Client, noti.Text)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_notices_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var conn_id string
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id") // server connection

	switch req.Method {
		case http.MethodPost:
			var noti json_in_notice

			err = json.NewDecoder(req.Body).Decode(&noti)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			err = s.SendNotice(conn_id, noti.Text)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_stats) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var stats json_out_server_stats
			stats.from_runtime_stats()
			stats.ServerConns = s.stats.conns.Load()
			stats.ServerRoutes = s.stats.routes.Load()
			stats.ServerPeers = s.stats.peers.Load()
			stats.SshProxySessions = s.stats.ssh_proxy_sessions.Load()
			stats.ServerPtsSessions = s.stats.pts_sessions.Load()
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(stats); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

func (ctl *server_ctl_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var wg sync.WaitGroup
	var sbsc *ServerEventSubscription
	var status_code int
	var err error
	var xerr error

	s = ctl.S

	// handle authentication using the first message.
	// end this task if authentication fails.
	if !ctl.NoAuth && s.Cfg.CtlAuth != nil {
		var req *http.Request

		req = ws.Request()
		if req.Header.Get("Authorization") == "" {
			var token string
			token = req.FormValue("token")
			if token != "" {
				// websocket doesn't actual have extra headers except a few fixed
				// ones. add "Authorization" header from the query paramerer and
				// compose a fake header to reuse the same Authentication() function
				req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
			}
		}

		status_code, _ = s.Cfg.CtlAuth.Authenticate(req)
		if status_code != http.StatusOK {
			goto done
		}
	}

	sbsc, err = s.bulletin.Subscribe("")
	if err != nil { goto done }

	wg.Add(1)
	go func() {
          var c chan *ServerEvent
		var err error

          defer wg.Done()
          c = sbsc.C

          for c != nil {
			var e *ServerEvent
			var ok bool
			var msg[] byte

			e, ok = <- c
			if ok {
				msg, err = json.Marshal(e)
				if err != nil {
					xerr = fmt.Errorf("failed to marshal event - %+v - %s", e, err.Error())
					c = nil
				} else {
					err = websocket.Message.Send(ws, msg)
					if err != nil {
						xerr = fmt.Errorf("failed to send message - %s", err.Error())
						c = nil
					}
				}
			} else {
				// most likely sbcs.C is closed. if not readable, break the loop
				c = nil
			}
		}

		ws.Close() // hack to break the recv loop. don't care about double closes
     }()

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil { break ws_recv_loop }

		if len(msg) > 0 {
			// do nothing. discard received messages
		}
	}

	// Ubsubscribe() to break the internal event reception
	// goroutine as well as for cleanup
	s.bulletin.Unsubscribe(sbsc)

done:
	ws.Close()
	wg.Wait()
	if err == nil && xerr != nil { err = xerr }
	return http.StatusOK, err
}
