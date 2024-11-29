package hodu

import "encoding/json"
import "net/http"
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
	ServerAddr string `json:"server-addr"`
}

type json_in_client_route struct {
	ClientPeerAddr string `json:"client-peer-addr"`
}

type json_out_client_conn_id struct {
	Id uint32 `json:"id"`
}

type json_out_client_conn struct {
	Id uint32 `json:"id"`
	ServerAddr string `json:"server-addr"`
	Routes []json_out_client_route `json:"routes"`
}

type json_out_client_route_id struct {
	Id uint32 `json:"id"`
}

type json_out_client_route struct {
	Id uint32 `json:"id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ServerPeerListenAddr string `json:"server-peer-listen-addr"`
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


type client_ctl_clients struct {
	c *Client
}

type client_ctl_clients_id struct {
	c *Client
}

// ------------------------------------

func (ctl *client_ctl_client_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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

			js = make([]json_out_client_conn, 0)
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map_by_id {
				var r *ClientRoute
				var jsp []json_out_client_route

				jsp = make([]json_out_client_route, 0)
				cts.route_mtx.Lock()
				for _, r = range cts.route_map {
					jsp = append(jsp, json_out_client_route{
						Id: r.id,
						ClientPeerAddr: r.peer_addr,
						ServerPeerListenAddr: r.server_peer_listen_addr.String(),
					})
				}
				js = append(js, json_out_client_conn{Id: cts.id, ServerAddr: cts.cfg.ServerAddr, Routes: jsp})
				cts.route_mtx.Unlock()
			}
			c.cts_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodPost:
			// add a new server connection
			var s json_in_client_conn
			var cc ClientConfig
			var cts *ClientConn

			err = json.NewDecoder(req.Body).Decode(&s)
			if err != nil {
				status_code = http.StatusBadRequest; w.WriteHeader(status_code)
				goto done
			}
			cc.ServerAddr = s.ServerAddr
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
			// delete all server conneections
			var cts *ClientConn
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map { cts.ReqStop() }
			c.cts_mtx.Unlock()
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


	c = ctl.c
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}


	switch req.Method {
		case http.MethodGet:
			var r *ClientRoute
			var jsp []json_out_client_route
			var js *json_out_client_conn
			var cts *ClientConn

			cts = c.FindClientConnById(uint32(conn_nid))
			if cts == nil {
				status_code = http.StatusNotFound; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
				goto done
			}

			jsp = make([]json_out_client_route, 0)
			cts.route_mtx.Lock()
			for _, r = range cts.route_map {
				jsp = append(jsp, json_out_client_route{
					Id: r.id,
					ClientPeerAddr: r.peer_addr,
					ServerPeerListenAddr: r.server_peer_listen_addr.String(),
				})
			}
			js = &json_out_client_conn{Id: cts.id, ServerAddr: cts.cfg.ServerAddr, Routes: jsp}
			cts.route_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
		/* TODO
			err = c.RemoveClientConnById(uint32(conn_nid))
			if err != nil {
				status_code = http.StatusNotFound; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
			} else {
				status_code = http.StatusNoContent; w.WriteHeader(status_code)
			}
		*/
		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}
	return


done:
	// TODO: need to handle x-forwarded-for and other stuff? this is not a real web service, though
	c.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	c.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return

}

func (ctl *client_ctl_client_conns_id_routes) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
				})
			}
			cts.route_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(jsp); err != nil { goto oops }

		case http.MethodPost:
			var jcr json_in_client_route
			var r *ClientRoute

			err = json.NewDecoder(req.Body).Decode(&jcr)
			if err != nil {
				status_code = http.StatusBadRequest; w.WriteHeader(status_code)
				goto done
			}

			r, err = cts.AddNewClientRoute(jcr.ClientPeerAddr, ROUTE_PROTO_TCP) // TODO: configurable protocol
			if err != nil {
				status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
			} else {
				status_code = http.StatusCreated; w.WriteHeader(status_code)
				if err = je.Encode(json_out_client_route_id{Id: r.id}); err != nil { goto oops }
			}

		case http.MethodDelete:
			cts.RemoveClientRoutes()
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
			err = cts.RemoveClientRouteById(uint32(route_nid))
			if err != nil {
				status_code = http.StatusNotFound; w.WriteHeader(status_code)
				if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
			} else {
				status_code = http.StatusNoContent; w.WriteHeader(status_code)
			}

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

func (ctl *client_ctl_clients) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

// ------------------------------------

func (ctl *client_ctl_clients_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}
