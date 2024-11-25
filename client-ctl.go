package hodu

import "encoding/json"
import "fmt"
import "net/http"


/*
 *                       POST                 GET            PUT            DELETE
 * /servers -     create new server     list all servers    bulk update     delete all servers
 * /servers/1 -        X             get server 1 details  update server 1  delete server 1
 * /servers/1/xxx -
 */



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


func (ctl *client_ctl_servers) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var c *Client
	var err error
	var ptn string

	c = ctl.c

	_, ptn = c.mux.Handler(req);
	fmt.Printf("%s %s %s [%s]\n", ptn, req.Method, req.URL.String(), req.PathValue("id"))

	switch req.Method {
		case http.MethodGet:
			goto bad_request // TODO:

		case http.MethodPost:
			var s ClientCtlParamServer
			var cc ClientConfig
			err = json.NewDecoder(req.Body).Decode(&s)
			if err != nil {
		fmt.Printf ("failed to decode body - %s\n", err.Error())
				goto bad_request
			}
			cc.ServerAddr = s.ServerAddr
			cc.PeerAddrs = s.PeerAddrs
			c.StartService(&cc) // TODO: this can be blocking. do we have to resolve addresses before calling this? also not good because resolution succeed or fail at each attempt.  however ok as ServeHTTP itself is in a goroutine?
			w.WriteHeader(http.StatusCreated)

		case http.MethodPut:
			goto bad_request // TODO:

		case http.MethodDelete:
			var cts *ClientConn
			c.cts_mtx.Lock()
			for _, cts = range c.cts_map { cts.ReqStop() }
			c.cts_mtx.Unlock()
	}

	return

bad_request:
	w.WriteHeader(http.StatusBadRequest)
	return

}


func (ctl *client_ctl_servers_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}


func (ctl *client_ctl_clients) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}

func (ctl *client_ctl_clients_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
}
