package hodu

import "encoding/json"
import "fmt"
import "net/http"
import "strconv"
import "strings"
import "time"
import "unsafe"

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
	ServerAddrs []string `json:"server-addrs"`
}

type json_in_client_route struct {
	Id RouteId `json:"id"` // 0 for auto-assignement.
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientPeerName string `json:"client-peer-name"`
	ServerPeerOption string `json:"server-peer-option"`
	ServerPeerServiceAddr string `json:"server-peer-service-addr"` // desired listening address on the server side
	ServerPeerServiceNet string `json:"server-peer-service-net"` // permitted network in prefix notation
	Lifetime string `json:"lifetime"`
}

type json_in_client_route_update struct {
	Lifetime string `sjon:"lifetime"`
}

type json_out_client_conn_id struct {
	Id ConnId `json:"id"`
}

type json_out_client_conn struct {
	Id ConnId `json:"id"`
	ReqServerAddrs []string `json:"req-server-addrs"` // server addresses requested. may include a domain name
	CurrentServerIndex int `json:"current-server-index"`
	ServerAddr string `json:"server-addr"` // actual server address
	ClientAddr string `json:"client-addr"`
	Routes []json_out_client_route `json:"routes"`
}

type json_out_client_route_id struct {
	Id    RouteId `json:"id"`
	CtsId ConnId  `json:"conn-id"`
}

type json_out_client_route struct {
	Id RouteId `json:"id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientPeerName string `json:"client-peer-name"`
	ServerPeerOption string `json:"server-peer-option"`
	ServerPeerListenAddr string `json:"server-peer-service-addr"`
	ServerPeerNet string `json:"server-peer-service-net"`
	Lifetime string `json:"lifetime"`
	LifetimeStart int64 `json:"lifetime-start"`
}

type json_out_client_peer struct {
	Id PeerId `json:"id"`
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

type client_ctl_client_conns struct {
	client_ctl
	//c *Client
	//id string
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

type client_ctl_stats struct {
	client_ctl
}

// ------------------------------------

func (ctl *client_ctl) Id() string {
	return ctl.id
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
			var cts *ClientConn
			var js []json_out_client_conn
//			var q url.Values

//			q = req.URL.Query()

// TODO: brief listing vs full listing
//			if q.Get("brief") == "true" {
//			}

			js = make([]json_out_client_conn, 0)
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map {
				var r *ClientRoute
				var jsp []json_out_client_route

				jsp = make([]json_out_client_route, 0)
				cts.route_mtx.Lock()
				for _, r = range cts.route_map {
					jsp = append(jsp, json_out_client_route{
						Id: r.Id,
						ClientPeerAddr: r.PeerAddr,
						ClientPeerName: r.PeerName,
						ServerPeerListenAddr: r.server_peer_listen_addr.String(),
						ServerPeerNet: r.ServerPeerNet,
						ServerPeerOption: r.ServerPeerOption.String(),
						Lifetime: DurationToSecString(r.Lifetime),
						LifetimeStart: r.LifetimeStart.Unix(),
					})
				}
				js = append(js, json_out_client_conn{
					Id: cts.Id,
					ReqServerAddrs: cts.cfg.ServerAddrs,
					CurrentServerIndex: cts.cfg.Index,
					ServerAddr: cts.remote_addr,
					ClientAddr: cts.local_addr,
					Routes: jsp,
				})
				cts.route_mtx.Unlock()
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
			var s json_in_client_conn
			var cc ClientConfig
			var cts *ClientConn

			err = json.NewDecoder(req.Body).Decode(&s)
			if err != nil || len(s.ServerAddrs) <= 0 {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto done
			}

			cc.ServerAddrs = s.ServerAddrs
			//cc.PeerAddrs = s.PeerAddrs
			cts, err = c.start_service(&cc) // TODO: this can be blocking. do we have to resolve addresses before calling this? also not good because resolution succeed or fail at each attempt.  however ok as ServeHTTP itself is in a goroutine?
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			} else {
				status_code = WriteJsonRespHeader(w, http.StatusCreated)
				if err = je.Encode(json_out_client_conn_id{Id: cts.Id}); err != nil { goto oops }
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
	var err error
	var conn_id string
	var conn_nid uint64
	var je *json.Encoder
	var cts *ClientConn

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong connection id - " + conn_id})
		goto oops
	}

	cts = c.FindClientConnById(ConnId(conn_nid))
	if cts == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent connection id - " + conn_id})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var r *ClientRoute
			var jsp []json_out_client_route
			var js *json_out_client_conn

			jsp = make([]json_out_client_route, 0)
			cts.route_mtx.Lock()
			for _, r = range cts.route_map {
				jsp = append(jsp, json_out_client_route{
					Id: r.Id,
					ClientPeerAddr: r.PeerAddr,
					ClientPeerName: r.PeerName,
					ServerPeerListenAddr: r.server_peer_listen_addr.String(),
					ServerPeerNet: r.ServerPeerNet,
					ServerPeerOption: r.ServerPeerOption.String(),
					Lifetime: DurationToSecString(r.Lifetime),
					LifetimeStart: r.LifetimeStart.Unix(),
				})
			}
			js = &json_out_client_conn{
				Id: cts.Id,
				ReqServerAddrs: cts.cfg.ServerAddrs,
				CurrentServerIndex: cts.cfg.Index,
				ServerAddr: cts.local_addr,
				ClientAddr: cts.remote_addr,
				Routes: jsp,
			}
			cts.route_mtx.Unlock()

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
	var err error
	var conn_id string
	var conn_nid uint64
	var je *json.Encoder
	var cts *ClientConn

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong connection id - " + conn_id })
		goto oops
	}

	cts = c.FindClientConnById(ConnId(conn_nid))
	if cts == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent connection id - " + conn_id})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var r *ClientRoute
			var jsp []json_out_client_route

			jsp = make([]json_out_client_route, 0)
			cts.route_mtx.Lock()
			for _, r = range cts.route_map {
				jsp = append(jsp, json_out_client_route{
					Id: r.Id,
					ClientPeerAddr: r.PeerAddr,
					ClientPeerName: r.PeerName,
					ServerPeerListenAddr: r.server_peer_listen_addr.String(),
					ServerPeerNet: r.ServerPeerNet,
					ServerPeerOption: r.ServerPeerOption.String(),
					Lifetime: DurationToSecString(r.Lifetime),
					LifetimeStart: r.LifetimeStart.Unix(),
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
				Id: jcr.Id,
				PeerAddr: jcr.ClientPeerAddr,
				PeerName: jcr.ClientPeerName,
				Option: server_peer_option,
				ServiceAddr: jcr.ServerPeerServiceAddr,
				ServiceNet: jcr.ServerPeerServiceNet,
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
				if err = je.Encode(json_out_client_route_id{Id: r.Id, CtsId: r.cts.Id}); err != nil { goto oops }
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
	var err error
	var conn_id string
	var route_id string
	var conn_nid uint64
	var route_nid uint64
	var je *json.Encoder
	var cts *ClientConn
	var r *ClientRoute

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong connection id - " + conn_id})
		goto oops
	}
	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong route id - " + route_id})
		goto oops
	}

	cts = c.FindClientConnById(ConnId(conn_nid))
	if cts == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent connection id - " + conn_id})
		goto oops
	}

	r = cts.FindClientRouteById(RouteId(route_nid))
	if r == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent route id - " + route_id})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			err = je.Encode(json_out_client_route{
				Id: r.Id,
				ClientPeerAddr: r.PeerAddr,
				ClientPeerName: r.PeerName,
				ServerPeerListenAddr: r.server_peer_listen_addr.String(),
				ServerPeerNet: r.ServerPeerNet,
				ServerPeerOption: r.ServerPeerOption.String(),
				Lifetime: DurationToSecString(r.Lifetime),
				LifetimeStart: r.LifetimeStart.Unix(),

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
	var err error
	var conn_id string
	var port_id string
	var conn_nid uint64
	var port_nid uint64
	var je *json.Encoder
	var cts *ClientConn
	var r *ClientRoute

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	port_id = req.PathValue("port_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong connection id - " + conn_id})
		goto oops
	}
	port_nid, err = strconv.ParseUint(port_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong route id - " + port_id})
		goto oops
	}

	cts = c.FindClientConnById(ConnId(conn_nid))
	if cts == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent connection id - " + conn_id})
		goto oops
	}

	r = cts.FindClientRouteByServerPeerSvcPortId(PortId(port_nid))
	if r == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent server peer port id - " + port_id})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			err = je.Encode(json_out_client_route{
				Id: r.Id,
				ClientPeerAddr: r.PeerAddr,
				ClientPeerName: r.PeerName,
				ServerPeerListenAddr: r.server_peer_listen_addr.String(),
				ServerPeerNet: r.ServerPeerNet,
				ServerPeerOption: r.ServerPeerOption.String(),
				Lifetime: DurationToSecString(r.Lifetime),
				LifetimeStart: r.LifetimeStart.Unix(),
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
	var err error
	var conn_id string
	var route_id string
	var conn_nid uint64
	var route_nid uint64
	var je *json.Encoder
	var r *ClientRoute

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong connection id - " + conn_id})
		goto oops
	}
	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong route id - " + route_id})
		goto oops
	}

	r = c.FindClientRouteById(ConnId(conn_nid), RouteId(route_nid))
	if r == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent connection/route id - " + conn_id + "/" + route_id})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var p *ClientPeerConn
			var jcp []json_out_client_peer

			jcp = make([]json_out_client_peer, 0)
			r.ptc_mtx.Lock()
			for _, p = range r.ptc_map {
				jcp = append(jcp, json_out_client_peer{
					Id: p.conn_id,
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
	var err error
	var conn_id string
	var route_id string
	var peer_id string
	var conn_nid uint64
	var route_nid uint64
	var peer_nid uint64
	var je *json.Encoder
	var p *ClientPeerConn

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	peer_id = req.PathValue("peer_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong connection id - " + conn_id})
		goto oops
	}
	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong route id - " + route_id})
		goto oops
	}
	peer_nid, err = strconv.ParseUint(peer_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
		je.Encode(JsonErrmsg{Text: "wrong peer id - " + peer_id})
		goto oops
	}

	p = c.FindClientPeerConnById(ConnId(conn_nid), RouteId(route_nid), PeerId(peer_nid))
	if p == nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: "non-existent connection/route/peer id - " + conn_id + "/" + route_id + "/" + peer_id})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var jcp *json_out_client_peer

			jcp = &json_out_client_peer{
				Id: p.conn_id,
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
