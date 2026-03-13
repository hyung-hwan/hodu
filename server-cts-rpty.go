package hodu

import "encoding/base64"
import "fmt"

import "golang.org/x/net/websocket"

// Rpty
func (cts *ServerConn) StartRpty(ws *websocket.Conn) (*ServerRpty, error) {
	var ok bool
	var start_id uint64
	var assigned_id uint64
	var rpty *ServerRpty
	var err error

	cts.rpty_mtx.Lock()
	start_id = cts.rpty_next_id
	for {
		_, ok = cts.rpty_map[cts.rpty_next_id]
		if !ok {
			assigned_id = cts.rpty_next_id
			cts.rpty_next_id++
			if cts.rpty_next_id == 0 { cts.rpty_next_id++ }
			break
		}
		cts.rpty_next_id++
		if cts.rpty_next_id == 0 { cts.rpty_next_id++ }
		if cts.rpty_next_id == start_id {
			cts.rpty_mtx.Unlock()
			return nil, fmt.Errorf("unable to assign id")
		}
	}

	_, ok = cts.rpty_map_by_ws[ws]
	if ok {
		cts.rpty_mtx.Unlock()
		return nil, fmt.Errorf("connection already associated with rpty. possibly internal error")
	}

	rpty = &ServerRpty{
		id: assigned_id,
		ws: ws,
	}

	cts.rpty_map[assigned_id] = rpty
	cts.rpty_map_by_ws[ws] = rpty
	cts.rpty_mtx.Unlock()

	err = cts.pss.Send(MakeRptyStartPacket(assigned_id))
	if err != nil {
		cts.rpty_mtx.Lock()
		delete(cts.rpty_map, assigned_id)
		delete(cts.rpty_map_by_ws, ws)
		cts.rpty_mtx.Unlock()
		return nil , err
	}

	cts.S.stats.rpty_sessions.Add(1)
	return rpty, nil
}

func (cts *ServerConn) StopRpty(ws *websocket.Conn) error {
	// called by the websocket handler.
	var rpty *ServerRpty
	var id uint64
	var ok bool
	var err error

	cts.rpty_mtx.Lock()
	rpty, ok = cts.rpty_map_by_ws[ws]
	if !ok {
		cts.rpty_mtx.Unlock()
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Unknown websocket connection for rpty - websocket %v", ws.RemoteAddr())
		return fmt.Errorf("unknown websocket connection for rpty - %v", ws.RemoteAddr())
	}

	id = rpty.id
	cts.rpty_mtx.Unlock()

	// send the stop request to the client side
	err = cts.pss.Send(MakeRptyStopPacket(id, ""))
	if err !=  nil {
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s(%d) for server %s websocket %v - %s", PACKET_KIND_RPTY_STOP.String(), id, cts.RemoteAddr, ws.RemoteAddr(), err.Error())
		// carry on
	}

	// delete the rpty entry from the maps as the websocket
	// handler is ending
	cts.rpty_mtx.Lock()
	delete(cts.rpty_map, id)
	delete(cts.rpty_map_by_ws, ws)
	cts.rpty_mtx.Unlock()
	cts.S.stats.rpty_sessions.Add(-1)

	cts.S.log.Write(cts.Sid, LOG_INFO, "Stopped rpty(%d) for server %s websocket %vs", id, cts.RemoteAddr, ws.RemoteAddr())
	return nil
}

func (cts *ServerConn) StopRptyWsById(id uint64, msg string) error {
	// call this when the stop requested comes from the client.
	// abort the websocket side.

	var rpty *ServerRpty
	var ok bool

	cts.rpty_mtx.Lock()
	rpty, ok = cts.rpty_map[id]
	if !ok {
		cts.rpty_mtx.Unlock()
		return fmt.Errorf("unknown rpty id %d", id)
	}
	cts.rpty_mtx.Unlock()

	rpty.ReqStop()
	cts.S.log.Write(cts.Sid, LOG_INFO, "Stopped rpty(%d) for %s - %s", id, cts.RemoteAddr, msg)
	return nil
}

func (cts *ServerConn) WriteRpty(ws *websocket.Conn, data []byte) error {
	var rpty *ServerRpty
	var id uint64
	var ok bool
	var err error

	cts.rpty_mtx.Lock()
	rpty, ok = cts.rpty_map_by_ws[ws]
	if !ok {
		cts.rpty_mtx.Unlock()
		return fmt.Errorf("unknown ws connection for rpty - %v", ws.RemoteAddr())
	}

	id = rpty.id
	cts.rpty_mtx.Unlock()

	err = cts.pss.Send(MakeRptyDataPacket(id, data))
	if err != nil {
		return fmt.Errorf("unable to send rpty data to client - %s", err.Error())
	}

	return nil
}

func (cts *ServerConn) WriteRptySize(ws *websocket.Conn, data []byte) error {
	var rpty *ServerRpty
	var id uint64
	var ok bool
	var err error

	cts.rpty_mtx.Lock()
	rpty, ok = cts.rpty_map_by_ws[ws]
	if !ok {
		cts.rpty_mtx.Unlock()
		return fmt.Errorf("unknown ws connection for rpty size - %v", ws.RemoteAddr())
	}

	id = rpty.id
	cts.rpty_mtx.Unlock()

	err = cts.pss.Send(MakeRptySizePacket(id, data))
	if err != nil {
		return fmt.Errorf("unable to send rpty size to client - %s", err.Error())
	}

	return nil
}

func (cts *ServerConn) ReadRptyAndWriteWs(id uint64, data []byte) error {
	var ok bool
	var rpty *ServerRpty
	var err error

	cts.rpty_mtx.Lock()
	rpty, ok = cts.rpty_map[id]
	if !ok {
		cts.rpty_mtx.Unlock()
		return fmt.Errorf("unknown rpty id - %d", id)
	}
	cts.rpty_mtx.Unlock()

	err = send_ws_data_for_xterm(rpty.ws, "iov", base64.StdEncoding.EncodeToString(data))
	if err != nil {
		return fmt.Errorf("failed to write rpty data(%d) to ws - %s", id, err.Error())
	}

	return nil
}

func (cts *ServerConn) HandleRptyEvent(packet_type PACKET_KIND, evt *RptyEvent) error {
	switch packet_type {
		case PACKET_KIND_RPTY_STOP:
			// stop requested from the server
			return cts.StopRptyWsById(evt.Id, string(evt.Data))

		case PACKET_KIND_RPTY_DATA:
			return cts.ReadRptyAndWriteWs(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}
