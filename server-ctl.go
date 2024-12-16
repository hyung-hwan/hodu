package hodu

import "encoding/json"
import "net/http"
import "runtime"
import "strconv"
import "unsafe"

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

type server_ctl_server_conns struct {
	s *Server
}

type server_ctl_server_conns_id struct {
	s *Server
}

type server_ctl_server_conns_id_routes struct {
	s *Server
}

type server_ctl_server_conns_id_routes_id struct {
	s *Server
}

type server_ctl_stats struct {
	s *Server
}

// ------------------------------------

func (ctl *server_ctl_server_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var status_code int
	var err error
	var je *json.Encoder

	// this deferred function is to overcome the recovering implemenation
	// from panic done in go's http server. in that implemenation, panic
	// is isolated to a single gorountine. however, i want this program
	// to exit immediately once a panic condition is caught. (e.g. nil
	// pointer dererence)
	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.s.log, req, err) }
	}()

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
						ClientPeerName: r.ptc_name,
						ServerPeerServiceAddr: r.svc_addr.String(),
						ServerPeerServiceNet: r.svc_permitted_net.String(),
						ServerPeerOption: r.svc_option.string(),
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

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.s.log, req, err) }
	}()

	s = ctl.s
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	cts = s.FindServerConnById(ConnId(conn_nid))
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
					ClientPeerName: r.ptc_name,
					ServerPeerServiceAddr: r.svc_addr.String(),
					ServerPeerServiceNet: r.svc_permitted_net.String(),
					ServerPeerOption: r.svc_option.string(),
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
	s.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code)
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id_routes) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var status_code int
	var err error
	var conn_id string
	var conn_nid uint64
	var je *json.Encoder
	var cts *ServerConn

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.s.log, req, err) }
	}()

	s = ctl.s
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		status_code = http.StatusBadRequest; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
		goto done
	}

	cts = s.FindServerConnById(ConnId(conn_nid))
	if cts == nil {
		status_code = http.StatusNotFound; w.WriteHeader(status_code)
		if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
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
					Id: r.id,
					ClientPeerAddr: r.ptc_addr,
					ClientPeerName: r.ptc_name,
					ServerPeerServiceAddr: r.svc_addr.String(),
					ServerPeerServiceNet: r.svc_permitted_net.String(),
					ServerPeerOption: r.svc_option.string(),
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
	s.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *server_ctl_server_conns_id_routes_id) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var status_code int
	var port_id string
	var conn_id string
	var route_id string
	var port_nid uint64
	var conn_nid uint64
	var route_nid uint64
	var je *json.Encoder
	var r *ServerRoute
	var err error

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.s.log, req, err) }
	}()

	s = ctl.s
	je = json.NewEncoder(w)

	conn_id = req.PathValue("conn_id")
	route_id = req.PathValue("route_id")
	if route_id == "_" {
		port_id = conn_id
		route_id = ""
	}

	if port_id != "" {
		// this condition is for ssh proxy server.
		port_nid, err = strconv.ParseUint(port_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			if err = je.Encode(json_errmsg{Text: "wrong port id - " + port_id}); err != nil { goto oops }
			goto done
		}

		r = s.FindServerRouteByPortId(PortId(port_nid))
		if r == nil {
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
			if err = je.Encode(json_errmsg{Text: "non-existent port id - " + port_id}); err != nil { goto oops }
		}
	} else {
		var cts *ServerConn

		conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			if err = je.Encode(json_errmsg{Text: "wrong connection id - " + conn_id}); err != nil { goto oops }
			goto done
		}
		route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
		if err != nil {
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
			if err = je.Encode(json_errmsg{Text: "wrong route id - " + route_id}); err != nil { goto oops }
			goto done
		}

		cts = s.FindServerConnById(ConnId(conn_nid))
		if cts == nil {
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
			if err = je.Encode(json_errmsg{Text: "non-existent connection id - " + conn_id}); err != nil { goto oops }
			goto done
		}

		r = cts.FindServerRouteById(RouteId(route_nid))
		if r == nil {
			status_code = http.StatusNotFound; w.WriteHeader(status_code)
			if err = je.Encode(json_errmsg{Text: "non-existent route id - " + conn_id}); err != nil { goto oops }
			goto done
		}
	}

	switch req.Method {
		case http.MethodGet:
			status_code = http.StatusOK; w.WriteHeader(status_code)
			err = je.Encode(json_out_server_route{
				Id: r.id,
				ClientPeerAddr: r.ptc_addr,
				ClientPeerName: r.ptc_name,
				ServerPeerServiceAddr: r.svc_addr.String(),
				ServerPeerServiceNet: r.svc_permitted_net.String(),
				ServerPeerOption: r.svc_option.string(),
			})
			if err != nil { goto oops }

		case http.MethodDelete:
			r.ReqStop()
			status_code = http.StatusNoContent; w.WriteHeader(status_code)

		default:
			status_code = http.StatusBadRequest; w.WriteHeader(status_code)
	}

done:
	// TODO: need to handle x-forwarded-for and other stuff? this is not a real web service, though
	s.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}

// ------------------------------------

func (ctl *server_ctl_stats) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var s *Server
	var status_code int
	var err error
	var je *json.Encoder

	defer func() {
		var err interface{} = recover()
		if err != nil { dump_call_frame_and_exit(ctl.s.log, req, err) }
	}()

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
	s.log.Write("", LOG_DEBUG, "[%s] %s %s %d", req.RemoteAddr, req.Method, req.URL.String(), status_code) // TODO: time taken
	return

oops:
	s.log.Write("", LOG_ERROR, "[%s] %s %s - %s", req.RemoteAddr, req.Method, req.URL.String(), err.Error())
	return
}
