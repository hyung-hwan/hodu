package hodu


import "encoding/base64"
import "fmt"

import "golang.org/x/net/websocket"


// Rxc - remote exec (server-side triggered, client-side executed)
func (cts *ServerConn) StartRxc(ws *websocket.Conn, kind string, script string) (*ServerRxc, error) {
	var ok bool
	var start_id uint64
	var assigned_id uint64
	var rxc *ServerRxc
	var pkt *Packet
	var err error

	cts.rxc_mtx.Lock()
	start_id = cts.rxc_next_id
	for {
		_, ok = cts.rxc_map[cts.rxc_next_id]
		if !ok {
			assigned_id = cts.rxc_next_id
			cts.rxc_next_id++
			if cts.rxc_next_id == 0 { cts.rxc_next_id++ }
			break
		}
		cts.rxc_next_id++
		if cts.rxc_next_id == 0 { cts.rxc_next_id++ }
		if cts.rxc_next_id == start_id {
			cts.rxc_mtx.Unlock()
			return nil, fmt.Errorf("unable to assign id")
		}
	}

	_, ok = cts.rxc_map_by_ws[ws]
	if ok {
		cts.rxc_mtx.Unlock()
		return nil, fmt.Errorf("connection already associated with rxc. possibly internal error")
	}

	rxc = &ServerRxc{
		id: assigned_id,
		ws: ws,
	}

	cts.rxc_map[assigned_id] = rxc
	cts.rxc_map_by_ws[ws] = rxc
	cts.rxc_mtx.Unlock()


	pkt, err = MakeRxcStartPacket(assigned_id, kind, script)
	if err != nil {
		return nil, fmt.Errorf("failed to make rxc start packet - %s", err.Error())
	}

	err = cts.pss.Send(pkt)
	if err != nil {
		cts.rxc_mtx.Lock()
		delete(cts.rxc_map, assigned_id)
		delete(cts.rxc_map_by_ws, ws)
		cts.rxc_mtx.Unlock()
		return nil , err
	}

	cts.S.stats.rxc_sessions.Add(1)
	return rxc, nil
}

func (cts *ServerConn) StopRxc(ws *websocket.Conn) error {
	// called by the websocket handler.
	var rxc *ServerRxc
	var id uint64
	var ok bool
	var err error

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map_by_ws[ws]
	if !ok {
		cts.rxc_mtx.Unlock()
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Unknown websocket connection for rxc - websocket %v", ws.RemoteAddr())
		return fmt.Errorf("unknown websocket connection for rxc - %v", ws.RemoteAddr())
	}

	id = rxc.id
	cts.rxc_mtx.Unlock()

	// send the stop request to the client side
	err = cts.pss.Send(MakeRxcStopPacket(id, ""))
	if err !=  nil {
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s(%d) for server %s websocket %v - %s", PACKET_KIND_RXC_STOP.String(), id, cts.RemoteAddr, ws.RemoteAddr(), err.Error())
		// carry on
	}

	// delete the rxc entry from the maps as the websocket
	// handler is ending
	cts.rxc_mtx.Lock()
	delete(cts.rxc_map, id)
	delete(cts.rxc_map_by_ws, ws)
	cts.rxc_mtx.Unlock()
	cts.S.stats.rxc_sessions.Add(-1)

	cts.S.log.Write(cts.Sid, LOG_INFO, "Stopped rxc(%d) for server %s websocket %vs", id, cts.RemoteAddr, ws.RemoteAddr())
	return nil
}

func (cts *ServerConn) StopRxcWsById(id uint64, msg string) error {
	// call this when the stop requested comes from the client.
	// abort the websocket side.

	var rxc *ServerRxc
	var ok bool

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map[id]
	if !ok {
		cts.rxc_mtx.Unlock()
		return fmt.Errorf("unknown rxc id %d", id)
	}
	cts.rxc_mtx.Unlock()

	rxc.ReqStop()
	cts.S.log.Write(cts.Sid, LOG_INFO, "Stopped rxc(%d) for %s - %s", id, cts.RemoteAddr, msg)
	return nil
}

func (cts *ServerConn) WriteRxc(ws *websocket.Conn, data []byte) error {
	var rxc *ServerRxc
	var id uint64
	var ok bool
	var err error

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map_by_ws[ws]
	if !ok {
		cts.rxc_mtx.Unlock()
		return fmt.Errorf("unknown ws connection for rxc - %v", ws.RemoteAddr())
	}

	id = rxc.id
	cts.rxc_mtx.Unlock()

	err = cts.pss.Send(MakeRxcDataPacket(id, data))
	if err != nil {
		return fmt.Errorf("unable to send rxc data to client - %s", err.Error())
	}

	return nil
}

func (cts *ServerConn) ReadRxcAndWriteWs(id uint64, data []byte) error {
	var ok bool
	var rxc *ServerRxc
	var err error

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map[id]
	if !ok {
		cts.rxc_mtx.Unlock()
		return fmt.Errorf("unknown rxc id - %d", id)
	}
	cts.rxc_mtx.Unlock()

	err = send_ws_data_for_xterm(rxc.ws, "iov", base64.StdEncoding.EncodeToString(data))
	if err != nil {
		return fmt.Errorf("failed to write rxc data(%d) to ws - %s", id, err.Error())
	}

	return nil
}

func (cts *ServerConn) HandleRxcEvent(packet_type PACKET_KIND, evt *RxcEvent) error {
	switch packet_type {
		case PACKET_KIND_RXC_STOP:
			// stop requested from the server
			return cts.StopRxcWsById(evt.Id, string(evt.Data))

		case PACKET_KIND_RXC_DATA:
			return cts.ReadRxcAndWriteWs(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}
