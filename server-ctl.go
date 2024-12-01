package hodu

import "net/http"

type server_ctl_client_conns struct {
	s *Server
}

// ------------------------------------

func (ctl *server_ctl_client_conns) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Write([]byte("hello"))
}
