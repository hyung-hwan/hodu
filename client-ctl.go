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
 */

type json_errmsg struct {
	Text string `json:"error-text"`
}

type json_in_peer_addrs struct {
	PeerAddrs []string `json:"peer-addrs"`
}

type json_out_server struct {
	Id uint32 `json:"id"`
	ServerAddr string `json:"server-addr"`
	PeerAddrs []json_out_server_peer `json:"peer-addrs"`
}

type json_out_server_peer struct {
	Id uint32 `json:"id"`
	ClientPeerAddr string `json:"peer-addr"`
	ServerPeerListenAddr string `json:"server-peer-listen-addr"`
}

// ------------------------------------

type client_ctl_servers struct {
	c *Client
}

type client_ctl_servers_id struct {
	c *Client
}

type client_ctl_servers_id_peers struct {
	c *Client
}

type client_ctl_clients struct {
	c *Client
}

type client_ctl_clients_id struct {
	c *Client
}

// ------------------------------------

func (ctl *client_ctl_servers) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error

	c = ctl.c

	switch req.Method {
		case http.MethodGet:
			var je *json.Encoder
			var cts *ClientConn
			var js []json_out_server

			status_code = http.StatusOK; w.WriteHeader(status_code)
			je = json.NewEncoder(w)

			c.cts_mtx.Lock()
			for _, cts = range c.cts_map_by_id {
				var r *ClientRoute
				var jsp []json_out_server_peer

				cts.route_mtx.Lock()
				for _, r = range cts.route_map {
					jsp = append(jsp, json_out_server_peer{
						Id: r.id,
						ClientPeerAddr: r.peer_addr.String(),
						ServerPeerListenAddr: r.server_peer_listen_addr.String(),
					})
				}
				js = append(js, json_out_server{Id: cts.id, ServerAddr: cts.saddr.String(), PeerAddrs: jsp})
				cts.route_mtx.Unlock()
			}
			c.cts_mtx.Unlock()

			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodPost:
			// add a new server connection
			var s ClientCtlParamServer
			var cc ClientConfig
			var cts *ClientConn

			err = json.NewDecoder(req.Body).Decode(&s)
			if err != nil {
				status_code = http.StatusBadRequest
				w.WriteHeader(status_code)
				goto done
			}
			cc.ServerAddr = s.ServerAddr
			cc.PeerAddrs = s.PeerAddrs
			cts, err = c.start_service(&cc) // TODO: this can be blocking. do we have to resolve addresses before calling this? also not good because resolution succeed or fail at each attempt.  however ok as ServeHTTP itself is in a goroutine?
			if err != nil {
				var je *json.Encoder
				status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
				je = json.NewEncoder(w)
				if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
			} else {
				var je *json.Encoder
				status_code = http.StatusCreated; w.WriteHeader(status_code)
				je = json.NewEncoder(w)
				if err = je.Encode(cts.cfg); err != nil { goto oops }
			}

		case http.MethodDelete:
			// delete all server conneections
			var cts *ClientConn
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map { cts.ReqStop() }
			c.cts_mtx.Unlock()
			w.WriteHeader(http.StatusNoContent)

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

// servers/{id}
func (ctl *client_ctl_servers_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {

	//req.PathValue("id")
	switch req.Method {
		case http.MethodGet:

		case http.MethodPost:

		case http.MethodPut: // update
			goto bad_request

		case http.MethodDelete:
	}
	return

bad_request:
	w.WriteHeader(http.StatusBadRequest)
	return
}

func (ctl *client_ctl_servers_id_peers) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var status_code int
	var err error
	var id string

	c = ctl.c

	id = req.PathValue("id")
	switch req.Method {
		case http.MethodGet:

		case http.MethodPost:
			var pa json_in_peer_addrs
			var cts *ClientConn
			var nid uint64

			err = json.NewDecoder(req.Body).Decode(&pa)
			if err != nil {
				status_code = http.StatusBadRequest; w.WriteHeader(status_code)
				goto done
			}

			nid, err = strconv.ParseUint(id, 10, 32)
			if err != nil {
				status_code = http.StatusBadRequest; w.WriteHeader(status_code)
				goto done
			}

			cts = c.FindClientConnById(uint32(nid))
			if cts == nil {
				status_code = http.StatusNotFound; w.WriteHeader(status_code)
			} else {
				err = cts.AddClientRoutes(pa.PeerAddrs)
				if err != nil {
					var je *json.Encoder
					status_code = http.StatusInternalServerError; w.WriteHeader(status_code)
					je = json.NewEncoder(w)
					if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
				} else {
					status_code = http.StatusCreated; w.WriteHeader(status_code)
				}
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
