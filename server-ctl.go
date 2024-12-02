package hodu

import "encoding/json"
import "net/http"


type json_out_server_conn struct {
	Id uint32 `json:"id"`
	ServerAddr string `json:"server-addr"`
	ClientAddr string `json:"client-addr"`
	Routes []json_out_server_route `json:"routes"`
}

type json_out_server_route struct {
	Id uint32 `json:"id"`
	ClientPeerAddr string `json:"client-peer-addr"`
	ServerPeerListenAddr string `json:"server-peer-listen-addr"`
}

// ------------------------------------

type server_ctl_server_conns struct {
	s *Server
}

type server_ctl_server_conns_id struct {
	s *Server
}

// ------------------------------------

func (ctl *server_ctl_server_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var status_code int
	var err error
	var je *json.Encoder

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
						Id: r.id,
						ClientPeerAddr: r.ptc_addr,
						ServerPeerListenAddr: r.laddr.String(),
					})
				}
				js = append(js, json_out_server_conn{Id: cts.id, ClientAddr: cts.caddr.String(), ServerAddr: cts.local_addr.String(), Routes: jsp})
				cts.route_mtx.Unlock()
			}
			s.cts_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			s.ReqStopAllServerConns()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

//done:
	s.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// TODO:
}
