package hodu

import "encoding/json"
import "net/http"
import "net/url"
import "runtime"
import "strconv"

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

type json_errmsg struct {
	Text string `json:"error-text"`
}

type json_in_client_conn struct {
	ServerAddrs []string `json:"server-addrs"`
}

type json_in_client_route struct {
	ClientPeerAddr string `json:"client-peer-addr"`
	ServerPeerNet string `json:"server-peer-net"` // allowed network in prefix notation
	ServerPeerProto ROUTE_PROTO `json:"server-peer-proto"`
}

type json_out_client_conn_id struct {
	Id uint32 `json:"id"`
}

type json_out_client_conn struct {
	Id uint32 `json:"id"`
	ReqServerAddrs []string `json:"req-server-addrs"` // server addresses requested. may include a domain name
	CurrentServerIndex int `json:"current-server-index"`
	ServerAddr string `json:"server-addr"` // actual server address
	ClientAddr string `json:"client-addr"`
	Routes []json_out_client_route `json:"routes"`
}

type json_out_client_route_id struct {
	Id uint32 `json:"id"`
}

type json_out_client_route struct {
	Id uint32 `json:"id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ServerPeerListenAddr string `json:"server-peer-listen-addr"`
	ServerPeerNet string `json:"server-peer-net"`
	ServerPeerProto ROUTE_PROTO `json:"server-peer-proto"`
}

type json_out_client_peer struct {
	Id uint32 `json:"id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ClientLocalAddr string `json:"client-local-addr"`
	ServerPeerAddr string `json:"server-peer-addr"`
	ServerLocalAddr string `json:"server-local-addr"`
}

type json_out_client_stats struct {
	CPUs int `json:"cpus"`
	Goroutines int `json:"goroutines"`
	ClientConns int64 `json:"client-conns"`
	ClientRoutes int64 `json:"client-routes"`
	ClientPeers int64 `json:"client-peers"`
}
// ------------------------------------

type client_ctl_client_conns struct {
	c *Client
}

type client_ctl_client_conns_id struct {
	c *Client
}

type client_ctl_client_conns_id_routes struct {
	c *Client
}

type client_ctl_client_conns_id_routes_id struct {
	c *Client
}

type client_ctl_client_conns_id_routes_id_peers struct {
	c *Client
}

type client_ctl_client_conns_id_routes_id_peers_id struct {
	c *Client
}

type client_ctl_stats struct {
	c *Client
}
// ------------------------------------

func (ctl *client_ctl_client_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var je *json.Encoder

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var cts *ClientConn
			var js []json_out_client_conn
			var q url.Values

			q = req.URL.Query()

// TODO: brief listing vs full listing
			if q.Get("brief") == "true" {
			}

			js = make([]json_out_client_conn, 0)
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map {
				var r *ClientRoute
				var jsp []json_out_client_route

				jsp = make([]json_out_client_route, 0)
				cts.route_mtx.Lock()
				for _, r = range cts.route_map {
					jsp = append(jsp, json_out_client_route{
						Id: r.id,
						ClientPeerAddr: r.peer_addr,
						ServerPeerListenAddr: r.server_peer_listen_addr.String(),
						ServerPeerNet: r.server_peer_net,
						ServerPeerProto: r.server_peer_proto,
					})
				}
				js = append(js, json_out_client_conn{
					Id: cts.id,
					ReqServerAddrs: cts.cfg.ServerAddrs,
					CurrentServerIndex: cts.cfg.Index,
					ServerAddr: cts.remote_addr,
					ClientAddr: cts.local_addr,
					Routes: jsp,
				})
				cts.route_mtx.Unlock()
			}
			c.cts_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
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
				status_code = http.StatusBadRequest; w.WriteHeader(status_code)
				goto done
			}

			cc.ServerAddrs = s.ServerAddrs
			//cc.PeerAddrs = s.PeerAddrs
			cts, err = c.start_service(&cc) // TODO: this can be blocking. do we have to resolve addresses before calling this? also not good because resolution succeed or fail at each attempt.  however ok as ServeHTTP itself is in a goroutine?
			if err != nil {
				status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
			} else {
				status_code = http.StatusCreated; w.WriteHeader(status_code)
				if err = je.Encode(json_out_client_conn_id{Id: cts.id}); err != nil { goto oops }
			}

		case http.MethodDelete:
			// delete all client connections to servers. if we request to stop all
			// client connections, they will remove themselves from the client.
			// we do passive deletion rather than doing active deletion by calling
			// c.RemoveAllClientConns()
			c.ReqStopAllClientConns()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	// TODO: need to handle x-forwarded-for and other stuff? this is not a real web service, though
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

// client-conns/{conn_id}
func (ctl *client_ctl_client_conns_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var conn_id string
	var conn_nid uint64
	var je *json.Encoder
	var cts *ClientConn

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	cts = c.FindClientConnById(uint32(conn_nid))
	if cts == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
		goto done
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
					Id: r.id,
					ClientPeerAddr: r.peer_addr,
					ServerPeerListenAddr: r.server_peer_listen_addr.String(),
					ServerPeerNet: r.server_peer_net,
					ServerPeerProto: r.server_peer_proto,
				})
			}
			js = &json_out_client_conn{
				Id: cts.id,
				ReqServerAddrs: cts.cfg.ServerAddrs,
				CurrentServerIndex: cts.cfg.Index,
				ServerAddr: cts.local_addr,
				ClientAddr: cts.remote_addr,
				Routes: jsp,
			}
			cts.route_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			//c.RemoveClientConn(cts)
			cts.ReqStop()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	// TODO: need to handle x-forwarded-for and other stuff? this is not a real web service, though
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *client_ctl_client_conns_id_routes) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var conn_id string
	var conn_nid uint64
	var je *json.Encoder
	var cts *ClientConn

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	cts = c.FindClientConnById(uint32(conn_nid))
	if cts == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	switch req.Method {
		case http.MethodGet:
			var r *ClientRoute
			var jsp []json_out_client_route

			jsp = make([]json_out_client_route, 0)
			cts.route_mtx.Lock()
			for _, r = range cts.route_map {
				jsp = append(jsp, json_out_client_route{
					Id: r.id,
					ClientPeerAddr: r.peer_addr,
					ServerPeerListenAddr: r.server_peer_listen_addr.String(),
					ServerPeerNet: r.server_peer_net,
					ServerPeerProto: r.server_peer_proto,
				})
			}
			cts.route_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(jsp); err != nil { goto oops }

		case http.MethodPost:
			var jcr json_in_client_route
			var r *ClientRoute

			err = json.NewDecoder(req.Body).Decode(&jcr)
			if err != nil || jcr.ClientPeerAddr == "" || jcr.ServerPeerProto < 0 || jcr.ServerPeerProto > ROUTE_PROTO_TCP6 {
				status_code = http.StatusBadRequest; w.WriteHeader(status_code)
				goto done
			}

			r, err = cts.AddNewClientRoute(jcr.ClientPeerAddr, jcr.ServerPeerNet, jcr.ServerPeerProto)
			if err != nil {
				status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
			} else {
				status_code = http.StatusCreated; w.WriteHeader(status_code)
				if err = je.Encode(json_out_client_route_id{Id: r.id}); err != nil { goto oops }
			}

		case http.MethodDelete:
			//cts.RemoveAllClientRoutes()
			cts.ReqStopAllClientRoutes()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	// TODO: need to handle x-forwarded-for and other stuff? this is not a real web service, though
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return

}

// ------------------------------------

func (ctl *client_ctl_client_conns_id_routes_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var conn_id string
	var route_id string
	var conn_nid uint64
	var route_nid uint64
	var je *json.Encoder
	var cts *ClientConn

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}
	route_nid, err = strconv.ParseUint(route_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong route id - " + route_id}); err != nil { goto oops }
		goto done
	}

	cts = c.FindClientConnById(uint32(conn_nid))
	if cts == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	switch req.Method {
		case http.MethodGet:
			var r *ClientRoute
			r = cts.FindClientRouteById(uint32(route_nid))
			if r == nil {
				status_code = http.StatusNotFound; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: "non-existent route id - " + conn_id}); err != nil { goto oops }
				goto done
			}
			err = je.Encode(json_out_client_route{
				Id: r.id,
				ClientPeerAddr: r.peer_addr,
				ServerPeerListenAddr: r.server_peer_listen_addr.String(),
			})
			if err != nil { goto oops }

		case http.MethodDelete:
			cts.ReqStop()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	// TODO: need to handle x-forwarded-for and other stuff? this is not a real web service, though
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *client_ctl_client_conns_id_routes_id_peers) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var conn_id string
	var route_id string
	var conn_nid uint64
	var route_nid uint64
	var je *json.Encoder
	var r *ClientRoute

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}
	route_nid, err = strconv.ParseUint(route_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong route id - " + route_id}); err != nil { goto oops }
		goto done
	}

	r = c.FindClientRouteById(uint32(conn_nid), uint32(route_nid))
	if r == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection/route id - " + conn_id + "/" + route_id}); err != nil { goto oops }
		goto done
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

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(jcp); err != nil { goto oops }

		case http.MethodDelete:
			r.ReqStopAllClientPeerConns()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}
done:
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *client_ctl_client_conns_id_routes_id_peers_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	peer_id = req.PathValue("peer_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}
	route_nid, err = strconv.ParseUint(route_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong route id - " + route_id}); err != nil { goto oops }
		goto done
	}
	peer_nid, err = strconv.ParseUint(peer_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong peer id - " + peer_id}); err != nil { goto oops }
		goto done
	}

	p = c.FindClientPeerConnById(uint32(conn_nid), uint32(route_nid), uint32(peer_nid))
	if p == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection/route/peer id - " + conn_id + "/" + route_id + "/" + peer_id}); err != nil { goto oops }
		goto done
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

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(jcp); err != nil { goto oops }

		case http.MethodDelete:
			p.ReqStop()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *client_ctl_stats) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var je *json.Encoder

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.c.log, req, err) }
	}()

	c = ctl.c
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var stats json_out_client_stats
			stats.CPUs = runtime.NumCPU()
			stats.Goroutines = runtime.NumGoroutine()
			stats.ClientConns = c.stats.conns.Load()
			stats.ClientRoutes = c.stats.routes.Load()
			stats.ClientPeers = c.stats.peers.Load()
			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(stats); err != nil { goto oops }

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

//done:
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}
