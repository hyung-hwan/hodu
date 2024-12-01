package hodu

import "fmt"
import "net/http"
import "golang.org/x/net/websocket"

type server_ctl_ws_tty struct {
	s *Server
	h websocket.Handler
}

func server_ws_tty (ws* websocket.Conn) {
	var msg []byte
	var err error

	ws.Write([]byte("hello world\r\n"))
	ws.Write([]byte("it's so wrong. it's awesome\r\n"))
	ws.Write([]byte("it's so wrong. 동키가 지나간다.it's awesome\r\n"))


	for {
		err = websocket.Message.Receive(ws, &msg)
		if err != nil {
			break
		} else if len(msg) == 0 {
			continue
		}

fmt.Printf ("RECEIVED MESSAGE [%v]\n", msg)
	}
}

func (ctl *server_ctl_ws_tty) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctl.h.ServeHTTP(w, req)
}





