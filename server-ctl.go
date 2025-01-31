package hodu

import "encoding/json"
import "fmt"
import "net/http"
import "net/netip"
import "path/filepath"
import "strings"
import "time"

type ServerTokenClaim struct {
	ExpiresAt int64 `json:"exp"`
	IssuedAt int64 `json:"iat"`
}

type json_out_token struct {
	AccessToken string `json:"access-token"`
	RefreshToken string `json:"refresh-token,omitempty"`
}

type json_out_server_conn struct {
	Id ConnId `json:"id"`
	ServerAddr string `json:"server-addr"`
	ClientAddr string `json:"client-addr"`
	Routes []json_out_server_route `json:"routes"`
}

type json_out_server_route struct {
	Id RouteId `json:"id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientPeerName string `json:"client-peer-name"`
	ServerPeerOption string `json:"server-peer-option"`
	ServerPeerServiceAddr string `json:"server-peer-service-addr"` // actual listening address
	ServerPeerServiceNet string `json:"server-peer-service-net"`
}

type json_out_server_stats struct {
	json_out_go_stats

	ServerConns int64 `json:"server-conns"`
	ServerRoutes int64 `json:"server-routes"`
	ServerPeers int64 `json:"server-peers"`

	SshProxySessions int64 `json:"pxy-ssh-sessions"`
}

// ------------------------------------

type server_ctl struct {
	s *Server
	id string
}

type server_ctl_token struct {
	server_ctl
}

type server_ctl_server_conns struct {
	server_ctl
}

type server_ctl_server_conns_id struct {
	server_ctl
}

type server_ctl_server_conns_id_routes struct {
	server_ctl
}

type server_ctl_server_conns_id_routes_id struct {
	server_ctl
}

type server_ctl_stats struct {
	server_ctl
}

// ------------------------------------

func (ctl *server_ctl) Id() string {
	return ctl.id
}

func (ctl *server_ctl) Authenticate(req *http.Request) (int, string) {
	var s *Server
	var rule HttpAccessRule
	var raddrport netip.AddrPort
	var raddr netip.Addr
	var err error

	s = ctl.s

	raddrport, err = netip.ParseAddrPort(req.RemoteAddr)
	if err == nil { raddr = raddrport.Addr() }

	for _, rule = range s.cfg.CtlAuth.AccessRules {
		// i don't take into account X-Forwarded-For and similar headers
		if req.URL.Path == rule.Prefix || strings.HasPrefix(req.URL.Path, filepath.Clean(rule.Prefix + "/")) {
			var org_net_ok bool

			if len(rule.OrgNets) > 0 && raddr.IsValid() {
				var netpfx netip.Prefix

				org_net_ok = false
				for  _, netpfx = range rule.OrgNets {
					if err == nil && netpfx.Contains(raddr) {
						org_net_ok = true
						break
					}
				}
			} else {
				org_net_ok = true
			}

			if org_net_ok {
				if rule.Action == HTTP_ACCESS_ACCEPT {
					return http.StatusOK, ""
				} else if rule.Action == HTTP_ACCESS_REJECT {
					return http.StatusForbidden, ""
				}
			}
		}
	}

	if s.cfg.CtlAuth != nil && s.cfg.CtlAuth.Enabled {
		var auth_hdr string
		var auth_parts []string
		var username string
		var password string
		var credpass string
		var ok bool
		var err error

		auth_hdr = req.Header.Get("Authorization")
		if auth_hdr == "" { return http.StatusUnauthorized, s.cfg.CtlAuth.Realm }

		auth_parts = strings.Fields(auth_hdr)
		if len(auth_parts) == 2 && strings.EqualFold(auth_parts[0], "Bearer") && s.cfg.CtlAuth.TokenRsaKey != nil {
			var jwt *JWT[ServerTokenClaim]
			var claim ServerTokenClaim
			jwt = NewJWT(s.cfg.CtlAuth.TokenRsaKey, &claim)
			err = jwt.VerifyRS512(strings.TrimSpace(auth_parts[1]))
			if err == nil {
				// verification ok. let's check the actual payload
				var now time.Time
				now = time.Now()
				if now.After(time.Unix(claim.IssuedAt, 0)) && now.Before(time.Unix(claim.ExpiresAt, 0)) { return http.StatusOK, "" } // not expired
			}
		}

		// fall back to basic authentication
		username, password, ok = req.BasicAuth()
		if !ok { return http.StatusUnauthorized, s.cfg.CtlAuth.Realm }

		credpass, ok = s.cfg.CtlAuth.Creds[username]
		if !ok || credpass != password { return http.StatusUnauthorized, s.cfg.CtlAuth.Realm }
	}

	return http.StatusOK, ""
}

// ------------------------------------

func (ctl *server_ctl_token) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.s
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var jwt *JWT[ServerTokenClaim]
			var claim ServerTokenClaim
			var tok string
			var now time.Time

			if s.cfg.CtlAuth == nil || !s.cfg.CtlAuth.Enabled || s.cfg.CtlAuth.TokenRsaKey == nil {
				status_code = WriteJsonRespHeader(w, http.StatusForbidden)
				err = fmt.Errorf("auth not enabled or token rsa key not set")
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			now = time.Now()
			claim.IssuedAt = now.Unix()
			claim.ExpiresAt = now.Add(s.cfg.CtlAuth.TokenTtl).Unix()
			jwt = NewJWT(s.cfg.CtlAuth.TokenRsaKey, &claim)
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
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.s
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var cts *ServerConn
			var js []json_out_server_conn

			js = make([]json_out_server_conn, 0)
			s.cts_mtx.Lock()
			for _, cts = range s.cts_map {
				var r *ServerRoute
				var jsp []json_out_server_route

				jsp = make([]json_out_server_route, 0)
				cts.route_mtx.Lock()
				for _, r = range cts.route_map {
					jsp = append(jsp, json_out_server_route{
						Id: r.Id,
						ClientPeerAddr: r.PtcAddr,
						ClientPeerName: r.PtcName,
						ServerPeerServiceAddr: r.SvcAddr.String(),
						ServerPeerServiceNet: r.SvcPermNet.String(),
						ServerPeerOption: r.SvcOption.String(),
					})
				}
				js = append(js, json_out_server_conn{
					Id: cts.Id,
					ClientAddr: cts.RemoteAddr.String(),
					ServerAddr: cts.LocalAddr.String(),
					Routes: jsp,
				})
				cts.route_mtx.Unlock()
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
	var status_code int
	var err error
	var je *json.Encoder
	var conn_id string
	var cts *ServerConn

	s = ctl.s
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
			var r *ServerRoute
			var jsp []json_out_server_route
			var js *json_out_server_conn

			jsp = make([]json_out_server_route, 0)
			cts.route_mtx.Lock()
			for _, r = range cts.route_map {
				jsp = append(jsp, json_out_server_route{
					Id: r.Id,
					ClientPeerAddr: r.PtcAddr,
					ClientPeerName: r.PtcName,
					ServerPeerServiceAddr: r.SvcAddr.String(),
					ServerPeerServiceNet: r.SvcPermNet.String(),
					ServerPeerOption: r.SvcOption.String(),
				})
			}
			js = &json_out_server_conn{
				Id: cts.Id,
				ClientAddr: cts.RemoteAddr.String(),
				ServerAddr: cts.LocalAddr.String(),
				Routes: jsp,
			}
			cts.route_mtx.Unlock()

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

	s = ctl.s
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
			var r *ServerRoute
			var jsp []json_out_server_route

			jsp = make([]json_out_server_route, 0)
			cts.route_mtx.Lock()
			for _, r = range cts.route_map {
				jsp = append(jsp, json_out_server_route{
					Id: r.Id,
					ClientPeerAddr: r.PtcAddr,
					ClientPeerName: r.PtcName,
					ServerPeerServiceAddr: r.SvcAddr.String(),
					ServerPeerServiceNet: r.SvcPermNet.String(),
					ServerPeerOption: r.SvcOption.String(),
				})
			}
			cts.route_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jsp); err != nil { goto oops }

		case http.MethodDelete:
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

	s = ctl.s
	je = json.NewEncoder(w)

	if ctl.id == HS_ID_WPX && req.Method != http.MethodGet {
		// support the get method only, if invoked via the wpx endpoint
		status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
		goto done
	}

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil {
		/*
		if route_id == PORT_ID_MARKER && ctl.s.wpx_foreign_port_proxy_marker != nil {
			// don't care if the ctl call is from wpx or not. if the request
			// is by the port number(noted by route being PORT_ID_MARKER),
			// check if it's a foreign port
			var pi *ServerRouteProxyInfo
			// currenly, this is invoked via wpx only for ssh from xterm.html
			// ugly, but hard-code the type to "ssh" here for now...
			pi, err = ctl.s.wpx_foreign_port_proxy_maker("ssh", conn_id)
			if err == nil { r = proxy_info_to_server_route(pi) } // fake route
		}
		*/

		if ctl.id == HS_ID_WPX && route_id == PORT_ID_MARKER && ctl.s.wpx_foreign_port_proxy_maker != nil {
			var pi *ServerRouteProxyInfo
			// currenly, this is invoked via wpx only for ssh from xterm.html
			// ugly, but hard-code the type to "ssh" here for now...
			pi, err = ctl.s.wpx_foreign_port_proxy_maker("ssh", conn_id)
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
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			err = je.Encode(json_out_server_route{
				Id: r.Id,
				ClientPeerAddr: r.PtcAddr,
				ClientPeerName: r.PtcName,
				ServerPeerServiceAddr: r.SvcAddr.String(),
				ServerPeerServiceNet: r.SvcPermNet.String(),
				ServerPeerOption: r.SvcOption.String(),
			})
			if err != nil { goto oops }

		case http.MethodDelete:
			/*if r is foreign {
				// foreign route
				ctl.s.wpx_foreign_port_proxy_stopper(conn_id)
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

func (ctl *server_ctl_stats) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var err error
	var je *json.Encoder

	s = ctl.s
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var stats json_out_server_stats
			stats.from_runtime_stats()
			stats.ServerConns = s.stats.conns.Load()
			stats.ServerRoutes = s.stats.routes.Load()
			stats.ServerPeers = s.stats.peers.Load()
			stats.SshProxySessions = s.stats.ssh_proxy_sessions.Load()
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(stats); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

//done:
	//s.log.Write(ctl.id, LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return status_code, nil

oops:
	//s.log.Write(ctl.id, LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return status_code, err
}
