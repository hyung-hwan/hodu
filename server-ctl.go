package hodu

import "encoding/json"
import "net/http"
import "strconv"


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
	ServerPeerNet string `json:"server-peer-net"`
	ServerPeerProto ROUTE_PROTO `json:"server-peer-proto"`
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
						ServerPeerListenAddr: r.svc_addr.String(),
						ServerPeerNet: r.svc_permitted_net.String(),
						ServerPeerProto: r.svc_proto,
					})
				}
				js = append(js, json_out_server_conn{
					Id: cts.id,
					ClientAddr: cts.remote_addr.String(),
					ServerAddr: cts.local_addr.String(),
					Routes: jsp,
				})
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
	var s *Server
	var status_code int
	var err error
	var je *json.Encoder
	var conn_id string
	var conn_nid uint64
	var cts *ServerConn

	s = ctl.s
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, 32)
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	cts = s.FindServerConnById(uint32(conn_nid))
	if cts == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
		goto done
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
					Id: r.id,
					ClientPeerAddr: r.ptc_addr,
					ServerPeerListenAddr: r.svc_addr.String(),
					ServerPeerNet: r.svc_permitted_net.String(),
					ServerPeerProto: r.svc_proto,
				})
			}
			js = &json_out_server_conn{
				Id: cts.id,
				ClientAddr: cts.remote_addr.String(),
				ServerAddr: cts.local_addr.String(),
				Routes: jsp,
			}
			cts.route_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			//s.RemoveServerConn(cts)
			cts.ReqStop()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	s.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}
