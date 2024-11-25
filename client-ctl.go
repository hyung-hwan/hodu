package hodu

import "encoding/json"
import "net/http"


/*
 *                       POST                 GET            PUT            DELETE
 * /servers -     create new server     list all servers    bulk update     delete all servers
 * /servers/1 -        X             get server 1 details  update server 1  delete server 1
 * /servers/1/xxx -
 * /servers/1112123/peers
 */

type http_errmsg struct {
	Text string `json:"error-text"`
}

type client_ctl_servers struct {
	c *Client
}

type client_ctl_servers_id struct {
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
			//var rc *http.ResponseController
			var cts *ClientConn
			var first bool = true

			//rc = http.NewResponseController(w)
			status_code = http.StatusOK; w.WriteHeader(status_code)
			je = json.NewEncoder(w)
			if _, err = w.Write([]byte("[")); err != nil { goto oops }
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map_by_id {
				if !first { w.Write([]byte(",")) }
				if err = je.Encode(cts.cfg); err != nil { goto oops }
				first = false
			}
			c.cts_mtx.Unlock()
			if _, err = w.Write([]byte("]")); err != nil { goto oops }
			//rc.Flush()

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
				if err = je.Encode(http_errmsg{Text: err.Error()}); err != nil { goto oops }
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

// ------------------------------------

func (ctl *client_ctl_clients) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

// ------------------------------------

func (ctl *client_ctl_clients_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}
