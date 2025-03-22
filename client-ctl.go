package hodu

import "encoding/json"
import "fmt"
import "net/http"
import "strings"
import "time"

/*
 *                       POST                 GET            PUT            DELETE
 * /servers -     create new server     list all servers    bulk update     delete all servers
 * /servers/1 -        X             get server 1 details  update server 1  delete server 1
 * /servers/1/xxx -
 *
 * /servers/1112123/peers
 *   POST add a new peer to a server
 *   GET list all peers
 *   PUT create/replace
 *   PATCH partial update
 * /servers/1112123/peers/1231344
 *   GET get info
 */

type json_in_client_conn struct {
	ServerAddrs []string `json:"server-addrs"` // multiple addresses for round-robin connection re-attempts
	ClientToken string `json:"client-token"`
	CloseOnConnErrorEvent bool `json:"close-on-conn-error-event"`
}

type json_in_client_route struct {
	RId RouteId `json:"route-id"` // 0 for auto-assignement.
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientPeerName string `json:"client-peer-name"`
	ServerPeerOption string `json:"server-peer-option"`

	// the following two fields in the input structure is the requested values.
	// the actual values are returned in json_out_clinet_route and may be different from the requested ones
	ServerPeerSvcAddr string `json:"server-peer-svc-addr"` // requested listening address on the server side - not actual
	ServerPeerSvcNet string `json:"server-peer-svc-net"` // requested permitted network in prefix notation - not actual

	Lifetime string `json:"lifetime"`
}

type json_in_client_route_update struct {
	Lifetime string `json:"lifetime"`
}

type json_out_client_conn_id struct {
	CId ConnId `json:"conn-id"`
}

type json_out_client_conn struct {
	CId ConnId `json:"conn-id"`
	ReqServerAddrs []string `json:"req-server-addrs"` // server addresses requested. may include a domain name
	CurrentServerIndex int `json:"current-server-index"`
	ServerAddr string `json:"server-addr"` // actual server address
	ClientAddr string `json:"client-addr"`
	ClientToken string `json:"client-token"`
	Routes []json_out_client_route `json:"routes"`
}

type json_out_client_route_id struct {
	CId  ConnId  `json:"conn-id"`
	RId  RouteId `json:"route-id"`
}

type json_out_client_route struct {
	CId ConnId `json:"conn-id"`
	RId RouteId `json:"route-id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientPeerName string `json:"client-peer-name"`
	ServerPeerOption string `json:"server-peer-option"`

	// These two values are actual addresses and networks listening on peer service port
	// and may be different from the requested values conveyed in json_in_client_route.
	ServerPeerSvcAddr string `json:"server-peer-svc-addr"`
	ServerPeerSvcNet string `json:"server-peer-svc-net"`

	Lifetime string `json:"lifetime"`
	LifetimeStart int64 `json:"lifetime-start"`
}

type json_out_client_peer struct {
	CId ConnId `json:"conn-id"`
	RId RouteId `json:"route-id"`
	PId PeerId `json:"peer-id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientLocalAddr string `json:"client-local-addr"`
	ServerPeerAddr string `json:"server-peer-addr"`
	ServerLocalAddr string `json:"server-local-addr"`
}

type json_out_client_stats struct {
	json_out_go_stats
	ClientConns int64 `json:"client-conns"`
	ClientRoutes int64 `json:"client-routes"`
	ClientPeers int64 `json:"client-peers"`
}
// ------------------------------------

type client_ctl struct {
	c *Client
	id string
}

type client_ctl_token struct {
	client_ctl
}

type client_ctl_client_conns struct {
	client_ctl
}

type client_ctl_client_conns_id struct {
	client_ctl
}

type client_ctl_client_conns_id_routes struct {
	client_ctl
}

type client_ctl_client_conns_id_routes_id struct {
	client_ctl
}

type client_ctl_client_conns_id_routes_spsp struct {
	client_ctl
}

type client_ctl_client_conns_id_routes_id_peers struct {
	client_ctl
}

type client_ctl_client_conns_id_routes_id_peers_id struct {
	client_ctl
}

type client_ctl_notices struct {
	client_ctl
}

type client_ctl_notices_id struct {
	client_ctl
}

type client_ctl_stats struct {
	client_ctl
}

// ------------------------------------

func (ctl *client_ctl) Id() string {
	return ctl.id
}

func (ctl *client_ctl) Cors(req *http.Request) bool {
	return ctl.c.ctl_cors
}

func (ctl *client_ctl) Authenticate(req *http.Request) (int, string) {
	if ctl.c.ctl_auth == nil { return http.StatusOK, "" }
     return ctl.c.ctl_auth.Authenticate(req)
}

// ------------------------------------

func (ctl *client_ctl_token) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var je *json.Encoder
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var jwt *JWT[ServerTokenClaim]
			var claim ServerTokenClaim
			var tok string
			var now time.Time

			if c.ctl_auth == nil || !c.ctl_auth.Enabled || c.ctl_auth.TokenRsaKey == nil {
				status_code = WriteJsonRespHeader(w, http.StatusForbidden)
				err = fmt.Errorf("auth not enabled or token rsa key not set")
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			now = time.Now()
			claim.IssuedAt = now.Unix()
			claim.ExpiresAt = now.Add(c.ctl_auth.TokenTtl).Unix()
			jwt = NewJWT(c.ctl_auth.TokenRsaKey, &claim)
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

func (ctl *client_ctl_client_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var err error
	var je *json.Encoder

	c = ctl.c
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var js []json_out_client_conn
			var ci ConnId
//			var q url.Values

//			q = req.URL.Query()

// TODO: brief listing vs full listing
//			if q.Get("brief") == "true" {
//			}

			js = make([]json_out_client_conn, 0)
			c.cts_mtx.Lock()
			for _, ci = range c.cts_map.get_sorted_keys() {
				var cts *ClientConn
				var jsp []json_out_client_route
				var ri RouteId

				cts = c.cts_map[ci]
				jsp = make([]json_out_client_route, 0)
				cts.route_mtx.Lock()
				for _, ri = range cts.route_map.get_sorted_keys() {
					var r *ClientRoute
					var lftsta time.Time
					var lftdur time.Duration

					r = cts.route_map[ri]

					lftsta, lftdur = r.GetLifetimeInfo()
					jsp = append(jsp, json_out_client_route{
						CId: cts.Id,
						RId: r.Id,
						ClientPeerAddr: r.PeerAddr,
						ClientPeerName: r.PeerName,
						ServerPeerSvcAddr: r.ServerPeerSvcAddr,
						ServerPeerSvcNet: r.ServerPeerSvcNet,
						ServerPeerOption: r.ServerPeerOption.String(),
						Lifetime: DurationToSecString(lftdur),
						LifetimeStart: lftsta.Unix(),
					})
				}
				cts.route_mtx.Unlock()

				js = append(js, json_out_client_conn{
					CId: cts.Id,
					ReqServerAddrs: cts.cfg.ServerAddrs,
					CurrentServerIndex: cts.cfg.Index,
					ServerAddr: cts.remote_addr.Get(),
					ClientAddr: cts.local_addr.Get(),
					ClientToken: cts.Token.Get(),
					Routes: jsp,
				})
			}
			c.cts_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodPost:
			// add a new client connection to a server with no client-side peers
			// while the client code can accept one more more client-side peer address
			// with the server added, i don't intentially accept peer addresses
			// as it's tricky to handle erroneous cases in creating the client routes
			// after hacing connected to the server. therefore, the json_in_client_conn
			// type contains a server address field only.
			var in_cc json_in_client_conn
			var cc ClientConnConfig
			var cts *ClientConn

			err = json.NewDecoder(req.Body).Decode(&in_cc)
			if err != nil || len(in_cc.ServerAddrs) <= 0 {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto done
			}

			cc.ServerAddrs = in_cc.ServerAddrs
			cc.ClientToken = in_cc.ClientToken
			cc.CloseOnConnErrorEvent = in_cc.CloseOnConnErrorEvent
			cts, err = c.start_service(&cc) // TODO: this can be blocking. do we have to resolve addresses before calling this? also not good because resolution succeed or fail at each attempt.  however ok as ServeHTTP itself is in a goroutine?
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			} else {
				status_code = WriteJsonRespHeader(w, http.StatusCreated)
				if err = je.Encode(json_out_client_conn_id{CId: cts.Id}); err != nil { goto oops }
			}

		case http.MethodDelete:
			// delete all client connections to servers. if we request to stop all
			// client connections, they will remove themselves from the client.
			// we do passive deletion rather than doing active deletion by calling
			// c.RemoveAllClientConns()
			c.ReqStopAllClientConns()
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
	}

done:
	return status_code, nil

oops:
	return status_code, err
}

// ------------------------------------

// client-conns/{conn_id}
func (ctl *client_ctl_client_conns_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var je *json.Encoder
	var cts *ClientConn
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	cts, err = c.FindClientConnByIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jsp []json_out_client_route
			var js *json_out_client_conn
			var ri RouteId

			jsp = make([]json_out_client_route, 0)
			cts.route_mtx.Lock()
			for _, ri = range cts.route_map.get_sorted_keys() {
				var r *ClientRoute
				var lftsta time.Time
				var lftdur time.Duration

				r = cts.route_map[ri]

				lftsta, lftdur = r.GetLifetimeInfo()
				jsp = append(jsp, json_out_client_route{
					CId: cts.Id,
					RId: r.Id,
					ClientPeerAddr: r.PeerAddr,
					ClientPeerName: r.PeerName,
					ServerPeerSvcAddr: r.ServerPeerSvcAddr,
					ServerPeerSvcNet: r.ServerPeerSvcNet,
					ServerPeerOption: r.ServerPeerOption.String(),
					Lifetime: DurationToSecString(lftdur),
					LifetimeStart: lftsta.Unix(),
				})
			}
			cts.route_mtx.Unlock()

			js = &json_out_client_conn{
				CId: cts.Id,
				ReqServerAddrs: cts.cfg.ServerAddrs,
				CurrentServerIndex: cts.cfg.Index,
				ServerAddr: cts.remote_addr.Get(),
				ClientAddr: cts.local_addr.Get(),
				ClientToken: cts.Token.Get(),
				Routes: jsp,
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			//c.RemoveClientConn(cts)
			cts.ReqStop()
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

func (ctl *client_ctl_client_conns_id_routes) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var je *json.Encoder
	var cts *ClientConn
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	cts, err = c.FindClientConnByIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jsp []json_out_client_route
			var ri RouteId

			jsp = make([]json_out_client_route, 0)
			cts.route_mtx.Lock()
			for _, ri = range cts.route_map.get_sorted_keys() {
				var r *ClientRoute
				var lftsta time.Time
				var lftdur time.Duration

				r = cts.route_map[ri]

				lftsta, lftdur = r.GetLifetimeInfo()
				jsp = append(jsp, json_out_client_route{
					CId: r.cts.Id,
					RId: r.Id,
					ClientPeerAddr: r.PeerAddr,
					ClientPeerName: r.PeerName,
					ServerPeerSvcAddr: r.ServerPeerSvcAddr,
					ServerPeerSvcNet: r.ServerPeerSvcNet,
					ServerPeerOption: r.ServerPeerOption.String(),
					Lifetime: DurationToSecString(lftdur),
					LifetimeStart: lftsta.Unix(),
				})
			}
			cts.route_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jsp); err != nil { goto oops }

		case http.MethodPost:
			var jcr json_in_client_route
			var r *ClientRoute
			var rc *ClientRouteConfig
			var server_peer_option RouteOption
			var lifetime time.Duration

			err = json.NewDecoder(req.Body).Decode(&jcr)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			if jcr.ClientPeerAddr == "" {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				err = fmt.Errorf("blank client-peer-addr")
				goto oops
			}

			server_peer_option = StringToRouteOption(jcr.ServerPeerOption)
			if server_peer_option == RouteOption(ROUTE_OPTION_UNSPEC) {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				err = fmt.Errorf("wrong server-peer-option value - %s", server_peer_option)
				goto oops
			}

			lifetime, err = ParseDurationString(jcr.Lifetime)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				err = fmt.Errorf("wrong lifetime value %s - %s", jcr.Lifetime, err.Error())
				goto oops
			}

			rc = &ClientRouteConfig{
				Id: jcr.RId,
				PeerAddr: jcr.ClientPeerAddr,
				PeerName: jcr.ClientPeerName,
				Option: server_peer_option,
				ServiceAddr: jcr.ServerPeerSvcAddr,
				ServiceNet: jcr.ServerPeerSvcNet,
				Lifetime: lifetime,
				Static: false,
			}

			r, err = cts.AddNewClientRoute(rc)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			} else {
				status_code = WriteJsonRespHeader(w, http.StatusCreated)
				if err = je.Encode(json_out_client_route_id{RId: r.Id, CId: r.cts.Id}); err != nil { goto oops }
			}

		case http.MethodDelete:
			//cts.RemoveAllClientRoutes()
			cts.ReqStopAllClientRoutes()
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

func (ctl *client_ctl_client_conns_id_routes_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var route_id string
	var je *json.Encoder
	var r *ClientRoute
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	r, err = c.FindClientRouteByIdStr(conn_id, route_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var lftsta time.Time
			var lftdur time.Duration

			status_code = WriteJsonRespHeader(w, http.StatusOK)

			lftsta, lftdur = r.GetLifetimeInfo()
			err = je.Encode(json_out_client_route{
				CId: r.cts.Id,
				RId: r.Id,
				ClientPeerAddr: r.PeerAddr,
				ClientPeerName: r.PeerName,
				ServerPeerSvcAddr: r.ServerPeerSvcAddr,
				ServerPeerSvcNet: r.ServerPeerSvcNet,
				ServerPeerOption: r.ServerPeerOption.String(),
				Lifetime: DurationToSecString(lftdur),
				LifetimeStart: lftsta.Unix(),
			})
			if err != nil { goto oops }

		case http.MethodPut:
			var jcr json_in_client_route_update
			var lifetime time.Duration

			err = json.NewDecoder(req.Body).Decode(&jcr)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			lifetime, err = ParseDurationString(jcr.Lifetime)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				err = fmt.Errorf("wrong lifetime value %s - %s", jcr.Lifetime, err.Error())
				goto oops
			}

			if strings.HasPrefix(jcr.Lifetime, "+") {
				err = r.ExtendLifetime(lifetime)
			} else {
				err = r.ResetLifetime(lifetime)
			}
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusForbidden)
				goto oops
			}

			status_code = WriteEmptyRespHeader(w, http.StatusOK)

		case http.MethodDelete:
			r.ReqStop()
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

func (ctl *client_ctl_client_conns_id_routes_spsp) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var port_id string
	var je *json.Encoder
	var r *ClientRoute
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	port_id = req.PathValue("port_id")
	r, err = c.FindClientRouteByServerPeerSvcPortIdStr(conn_id, port_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var lftsta time.Time
			var lftdur time.Duration

			status_code = WriteJsonRespHeader(w, http.StatusOK)

			lftsta, lftdur = r.GetLifetimeInfo()
			err = je.Encode(json_out_client_route{
				CId: r.cts.Id,
				RId: r.Id,
				ClientPeerAddr: r.PeerAddr,
				ClientPeerName: r.PeerName,
				ServerPeerSvcAddr: r.ServerPeerSvcAddr,
				ServerPeerSvcNet: r.ServerPeerSvcNet,
				ServerPeerOption: r.ServerPeerOption.String(),
				Lifetime: DurationToSecString(lftdur),
				LifetimeStart: lftsta.Unix(),
			})
			if err != nil { goto oops }

		case http.MethodPut:
			var jcr json_in_client_route_update
			var lifetime time.Duration

			err = json.NewDecoder(req.Body).Decode(&jcr)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			lifetime, err = ParseDurationString(jcr.Lifetime)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				err = fmt.Errorf("wrong lifetime value %s - %s", jcr.Lifetime, err.Error())
				goto oops
			} else if lifetime < 0 {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				err = fmt.Errorf("negative lifetime value %s", jcr.Lifetime)
				goto oops
			}

			if strings.HasPrefix(jcr.Lifetime, "+") {
				err = r.ExtendLifetime(lifetime)
			} else {
				err = r.ResetLifetime(lifetime)
			}
			if err != nil { goto oops }

		case http.MethodDelete:
			r.ReqStop()
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

func (ctl *client_ctl_client_conns_id_routes_id_peers) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var route_id string
	var je *json.Encoder
	var r *ClientRoute
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	r, err = c.FindClientRouteByIdStr(conn_id, route_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jcp []json_out_client_peer
			var pi PeerId

			jcp = make([]json_out_client_peer, 0)
			r.ptc_mtx.Lock()
			for _, pi = range r.ptc_map.get_sorted_keys() {
				var p *ClientPeerConn

				p = r.ptc_map[pi]
				jcp = append(jcp, json_out_client_peer{
					CId: r.cts.Id,
					RId: r.Id,
					PId: p.conn_id,
					ClientPeerAddr: p.conn.RemoteAddr().String(),
					ClientLocalAddr: p.conn.LocalAddr().String(),
					ServerPeerAddr: p.pts_raddr,
					ServerLocalAddr: p.pts_laddr,
				})
			}
			r.ptc_mtx.Unlock()

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jcp); err != nil { goto oops }

		case http.MethodDelete:
			r.ReqStopAllClientPeerConns()
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

func (ctl *client_ctl_client_conns_id_routes_id_peers_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var route_id string
	var peer_id string
	var je *json.Encoder
	var p *ClientPeerConn
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	peer_id = req.PathValue("peer_id")
	p, err = c.FindClientPeerConnByIdStr(conn_id, route_id, peer_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jcp *json_out_client_peer

			jcp = &json_out_client_peer{
				CId: p.route.cts.Id,
				RId: p.route.Id,
				PId: p.conn_id,
				ClientPeerAddr: p.conn.RemoteAddr().String(),
				ClientLocalAddr: p.conn.LocalAddr().String(),
				ServerPeerAddr: p.pts_raddr,
				ServerLocalAddr: p.pts_laddr,
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

func (ctl *client_ctl_notices) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var cts *ClientConn
	var err error

	c = ctl.c

	switch req.Method {
		case http.MethodPost:
			var noti json_in_notice

			err = json.NewDecoder(req.Body).Decode(&noti)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			c.cts_mtx.Lock()
			for _, cts = range c.cts_map {
				cts.psc.Send(MakeConnNoticePacket(noti.Text))
				// let's not care about an error when broacasting a notice to all connections
			}
			c.cts_mtx.Unlock()
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

func (ctl *client_ctl_notices_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var conn_id string
	var cts *ClientConn
	var je *json.Encoder
	var err error

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	cts, err = c.FindClientConnByIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodPost:
			var noti json_in_notice

			err = json.NewDecoder(req.Body).Decode(&noti)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			// no check if noti.Text is empty as i want an empty message to be delivered too.
			err = cts.psc.Send(MakeConnNoticePacket(noti.Text))
			if err != nil {
				err = fmt.Errorf("failed to send conn_notice text '%s' to %s - %s", noti.Text, cts.remote_addr, err.Error())
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

func (ctl *client_ctl_stats) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var err error
	var je *json.Encoder

	c = ctl.c
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var stats json_out_client_stats
			stats.from_runtime_stats()
			stats.ClientConns = c.stats.conns.Load()
			stats.ClientRoutes = c.stats.routes.Load()
			stats.ClientPeers = c.stats.peers.Load()
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(stats); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}
