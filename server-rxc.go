package hodu


import "encoding/base64"
import "encoding/json"
import "fmt"
import "net/http"
//import "strconv"
//import "strings"
import "sync"
//import "text/template"
import "time"

import "golang.org/x/net/websocket"

// rxc - remote exec
type server_rxc_ws struct {
	S *Server
	Id string
	ws *websocket.Conn
}

func (rxc *server_rxc_ws) Identity() string {
	return rxc.Id
}

func (rxc *server_rxc_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var token string
	var cts *ServerConn
	var rp *ServerRxc
	var wg sync.WaitGroup
	var err error

	s = rxc.S
	req = ws.Request()
	token = req.FormValue("client-token")
	if token == "" {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("no client token specified")
	}

	cts = s.FindServerConnByClientToken(token)
	if cts == nil {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("invalid client token - %s", token)
	}

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil {
			s.log.Write(rxc.Id, LOG_ERROR, "[%s] websocket receive error - %s", req.RemoteAddr, err.Error())
			goto done
		}

		if len(msg) > 0 {
			var ev json_xterm_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if rp == nil && len(ev.Data) == 2 {
							var kind string
							var script string

							kind = string(ev.Data[0])
							script = string(ev.Data[1])
							//options = string(ev.Data[2])

							rp, err = cts.StartRxc(ws, kind, script)
							if err != nil {
								s.log.Write(rxc.Id, LOG_ERROR, "[%s] Failed to connect rxc - %s", req.RemoteAddr, err.Error())
								send_ws_data_for_xterm(ws, "error", err.Error())
								//ws.Close() // dirty way to flag out the error by making websocket.Message.Receive() fail
								ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
							} else {
								err = send_ws_data_for_xterm(ws, "status", "opened")
								if err != nil {
									s.log.Write(rxc.Id, LOG_ERROR, "[%s] Failed to write 'opened' event to websocket - %s", req.RemoteAddr, err.Error())
									//ws.Close() // dirty way to flag out the error
									ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
								} else {
									s.log.Write(rxc.Id, LOG_DEBUG, "[%s] Opened rxc session", req.RemoteAddr)
								}
							}
						}

					case "close":
						// just break out of the loop and let the remainder to close resources
						break ws_recv_loop

					case "iov":
						var i int
						for i, _ = range ev.Data {
							//cts.WriteRxc(ws, []byte(ev.Data[i]))
							var bytes []byte
							bytes, err = base64.StdEncoding.DecodeString(ev.Data[i])
							if err != nil {
								s.log.Write(rxc.Id, LOG_WARN, "[%s] Invalid rxc iov data received - %s", req.RemoteAddr, ev.Data[i])
							} else {
								cts.WriteRxc(ws, bytes)
								// ignore error for now
							}
						}
				}
			}
		}
	}

done:
	cts.StopRxc(ws)
	ws.Close() // don't care about multiple closes

	wg.Wait()
	s.log.Write(rxc.Id, LOG_DEBUG, "[%s] Ended rxc session for %s", req.RemoteAddr, token)

	return http.StatusOK, err
}
