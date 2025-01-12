package hodu

import "encoding/json"
import "net/http"
import "runtime"

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
	CPUs int `json:"cpus"`
	Goroutines int `json:"goroutines"`

	NumGCs uint32 `json:"num-gcs"`
	HeapAllocBytes uint64 `json:"memory-alloc-bytes"`
	MemAllocs uint64 `json:"memory-num-allocs"`
	MemFrees uint64 `json:"memory-num-frees"`

	ServerConns int64 `json:"server-conns"`
	ServerRoutes int64 `json:"server-routes"`
	ServerPeers int64 `json:"server-peers"`

	SshProxySessions int64 `json:"ssh-proxy-session"`
}

// ------------------------------------

type server_ctl struct {
	s *Server
	id string
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

func (ctl *server_ctl) GetId() string {
	return ctl.id
}

// ------------------------------------

func (ctl *server_ctl_server_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
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
						Id: r.Id,
						ClientPeerAddr: r.PtcAddr,
						ClientPeerName: r.PtcName,
						ServerPeerServiceAddr: r.SvcAddr.String(),
						ServerPeerServiceNet: r.SvcPermNet.String(),
						ServerPeerOption: r.SvcOption.string(),
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

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(js); err != nil { goto oops }

		case http.MethodDelete:
			s.ReqStopAllServerConns()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
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
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
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
					Id: r.Id,
					ClientPeerAddr: r.PtcAddr,
					ClientPeerName: r.PtcName,
					ServerPeerServiceAddr: r.SvcAddr.String(),
					ServerPeerServiceNet: r.SvcPermNet.String(),
					ServerPeerOption: r.SvcOption.string(),
				})
			}
			js = &json_out_server_conn{
				Id: cts.Id,
				ClientAddr: cts.RemoteAddr.String(),
				ServerAddr: cts.LocalAddr.String(),
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
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
		goto done
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
					ServerPeerOption: r.SvcOption.string(),
				})
			}
			cts.route_mtx.Unlock()

			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(jsp); err != nil { goto oops }

		case http.MethodDelete:
			//cts.RemoveAllServerRoutes()
			cts.ReqStopAllServerRoutes()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
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

	if ctl.id == "wpx" && req.Method != http.MethodGet {
		// support the get method only, if invoked via the wpx endpoint
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		goto done
	}

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	r, err = s.FindServerRouteByIdStr(conn_id, route_id)
	if err != nil && ctl.id == "wpx" && route_id == PORT_ID_MARKER && ctl.s.wpx_foreign_port_proxy_maker != nil {
		var pi *ServerRouteProxyInfo
		// currenly, this is invoked via wpx only for ssh from xterm.html
		// ugly, but hard-code the type to "ssh" here for now...
		pi, err = ctl.s.wpx_foreign_port_proxy_maker("ssh", conn_id)
		if err == nil {
			// fake route
			r = &ServerRoute{
				SvcOption: pi.SvcOption,
				PtcName: pi.PtcName,
				PtcAddr: pi.PtcAddr,
				SvcAddr: pi.SvcAddr,
			}
		}
	}

	if err != nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: err.Error()}); err != nil { goto oops }
		goto done
	}

	switch req.Method {
		case http.MethodGet:
			status_code = http.StatusOK; w.WriteHeader(status_code)
			err = je.Encode(json_out_server_route{
				Id: r.Id,
				ClientPeerAddr: r.PtcAddr,
				ClientPeerName: r.PtcName,
				ServerPeerServiceAddr: r.SvcAddr.String(),
				ServerPeerServiceNet: r.SvcPermNet.String(),
				ServerPeerOption: r.SvcOption.string(),
			})
			if err != nil { goto oops }

		case http.MethodDelete:
			r.ReqStop()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
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
			var mstat runtime.MemStats
			runtime.ReadMemStats(&mstat)
			stats.CPUs = runtime.NumCPU()
			stats.Goroutines = runtime.NumGoroutine()
			stats.NumGCs = mstat.NumGC
			stats.HeapAllocBytes = mstat.HeapAlloc
			stats.MemAllocs = mstat.Mallocs
			stats.MemFrees = mstat.Frees
			stats.ServerConns = s.stats.conns.Load()
			stats.ServerRoutes = s.stats.routes.Load()
			stats.ServerPeers = s.stats.peers.Load()
			stats.SshProxySessions = s.stats.ssh_proxy_sessions.Load()
			status_code = http.StatusOK; w.WriteHeader(status_code)
			if err = je.Encode(stats); err != nil { goto oops }

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

//done:
	//s.log.Write(ctl.id, LOG_INFO, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return status_code, nil

oops:
	//s.log.Write(ctl.id, LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return status_code, err
}
