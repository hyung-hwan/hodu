package hodu

import "net/http"

type server_rpx struct {
	S *Server
	Id string
}

// ------------------------------------
func (pxy *server_rpx) Identity() string {
	return pxy.Id
}

func (pxy *server_rpx) Cors(req *http.Request) bool {
	return false
}

func (pxy *server_rpx) Authenticate(req *http.Request) (int, string) {
	return http.StatusOK, ""
}

func (pxy *server_rpx) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var status_code int
//	var err error

	status_code = http.StatusOK

//done:
	return status_code, nil

//oops:
//	return status_code, err
}

