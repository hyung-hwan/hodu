package hodu

// Rxc - remote exec (server-side triggered, client-side executed)

import "fmt"

import "golang.org/x/net/websocket"

func (cts *ServerConn) add_new_rxc(sink ServerRxcSink, ws *websocket.Conn, kind string, script string) (*ServerRxc, error) {
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

	if ws != nil {
		_, ok = cts.rxc_map_by_ws[ws]
		if ok {
			cts.rxc_mtx.Unlock()
			return nil, fmt.Errorf("connection already associated with rxc. possibly internal error")
		}
	}

	rxc = &ServerRxc{
		id: assigned_id,
		ws: ws,
		sink: sink,
	}

	cts.rxc_map[assigned_id] = rxc
	if ws != nil { cts.rxc_map_by_ws[ws] = rxc }
	cts.rxc_mtx.Unlock()

	pkt, err = MakeRxcStartPacket(assigned_id, kind, script)
	if err != nil {
		cts.rxc_mtx.Lock()
		delete(cts.rxc_map, assigned_id)
		if ws != nil { delete(cts.rxc_map_by_ws, ws) }
		cts.rxc_mtx.Unlock()
		return nil, fmt.Errorf("failed to make rxc start packet - %s", err.Error())
	}

	err = cts.pss.Send(pkt)
	if err != nil {
		cts.rxc_mtx.Lock()
		delete(cts.rxc_map, assigned_id)
		if ws != nil { delete(cts.rxc_map_by_ws, ws) }
		cts.rxc_mtx.Unlock()
		return nil, fmt.Errorf("failed to send rxc start packet - %s", err.Error())
	}

	cts.S.stats.rxc_sessions.Add(1)
	return rxc, nil
}

func (cts *ServerConn) StartRxcForWs(ws *websocket.Conn, kind string, script string) (*ServerRxc, error) {
	// start a single task over rxc for a websocket
	return cts.add_new_rxc(&ServerRxcWebsocketSink{ws: ws}, ws, kind, script)
}

func (cts *ServerConn) RunRxcJob(run *ServerRxcJobRun, kind string, script string) error {
	var rxc *ServerRxc
	var err error

	rxc, err = cts.add_new_rxc(run, nil, kind, script)
	if err != nil { return err }
	run.mark_started(rxc.id)
	return nil
}

func (cts *ServerConn) find_rxc_by_ws(ws *websocket.Conn) (*ServerRxc, bool) {
	var rxc *ServerRxc
	var ok bool

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map_by_ws[ws]
	cts.rxc_mtx.Unlock()
	return rxc, ok
}

func (cts *ServerConn) find_rxc_by_id(id uint64) (*ServerRxc, bool) {
	var rxc *ServerRxc
	var ok bool

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map[id]
	cts.rxc_mtx.Unlock()
	return rxc, ok
}

func (cts *ServerConn) remove_rxc_by_id(id uint64) (*ServerRxc, bool) {
	var rxc *ServerRxc
	var ok bool

	cts.rxc_mtx.Lock()
	rxc, ok = cts.rxc_map[id]
	if ok {
		delete(cts.rxc_map, id)
		if rxc.ws != nil {
			delete(cts.rxc_map_by_ws, rxc.ws)
		}
	}
	cts.rxc_mtx.Unlock()
	return rxc, ok
}

func (cts *ServerConn) StopRxcForWs(ws *websocket.Conn) error {
	// called by the websocket handler.
	var rxc *ServerRxc
	var ok bool
	var err error

	rxc, ok = cts.find_rxc_by_ws(ws)
	if !ok {
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Unknown websocket connection for rxc - websocket %v", ws.RemoteAddr())
		return fmt.Errorf("unknown websocket connection for rxc - %v", ws.RemoteAddr())
	}

	err = cts.pss.Send(MakeRxcStopPacket(rxc.id, ""))
	if err != nil {
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s(%d) for server %s websocket %v - %s", PACKET_KIND_RXC_STOP.String(), rxc.id, cts.RemoteAddr, ws.RemoteAddr(), err.Error())
	}

	_, ok = cts.remove_rxc_by_id(rxc.id)
	if ok {
		cts.S.stats.rxc_sessions.Add(-1)
	}

	rxc.ReqStop()
	cts.S.log.Write(cts.Sid, LOG_INFO, "Stopped rxc(%d) for server %s websocket %v", rxc.id, cts.RemoteAddr, ws.RemoteAddr())
	return nil
}

func (cts *ServerConn) SendStopRxcById(id uint64, flags uint32) error {
	var ok bool
	var err error

	_, ok = cts.find_rxc_by_id(id)
	if !ok {
		return fmt.Errorf("unknown rxc id %d", id)
	}

	err = cts.pss.Send(MakeRxcStopPacket(id, ""))
	if err != nil {
		return fmt.Errorf("failed to send %s(%d) to client - %s", PACKET_KIND_RXC_STOP.String(), id, err.Error())
	}

	return nil
}

func (cts *ServerConn) StopRxcSinkById(id uint64, flags uint32, msg string) error {
	var rxc *ServerRxc
	var ok bool
	var err error

	rxc, ok = cts.remove_rxc_by_id(id)
	if !ok {
		return fmt.Errorf("unknown rxc id %d", id)
	}
	cts.S.stats.rxc_sessions.Add(-1)

	if rxc.sink != nil {
		err = rxc.sink.Stop(msg)
		if err != nil { return err }
	}

	cts.S.log.Write(cts.Sid, LOG_INFO, "Stopped rxc job(%d) run for client(%s) from %s", id, cts.ClientToken.Get(), cts.RemoteAddr)
	return nil
}

func (cts *ServerConn) WriteRxcForWs(ws *websocket.Conn, data []byte) error {
	var rxc *ServerRxc
	var ok bool
	var err error

	rxc, ok = cts.find_rxc_by_ws(ws)
	if !ok { return fmt.Errorf("unknown ws connection for rxc - %v", ws.RemoteAddr()) }

	err = cts.pss.Send(MakeRxcDataPacket(rxc.id, RXC_DATA_FLAG_NONE, data))
	if err != nil { return fmt.Errorf("unable to send rxc data to client - %s", err.Error()) }

	return nil
}

func (cts *ServerConn) ReadRxcAndWriteSinkById(id uint64, flags uint32, data []byte) error {
	var rxc *ServerRxc
	var ok bool

	rxc, ok = cts.find_rxc_by_id(id)
	if !ok { return fmt.Errorf("unknown rxc id - %d", id) }
	if rxc.sink == nil { return fmt.Errorf("missing rxc sink for id %d", id) }

	return rxc.sink.Write(flags, data)
}

func (cts *ServerConn) HandleRxcEvent(packet_type PACKET_KIND, evt *RxcEvent) error {
	switch packet_type {
		case PACKET_KIND_RXC_STOP:
			return cts.StopRxcSinkById(evt.Id, evt.Flags, string(evt.Data))

		case PACKET_KIND_RXC_DATA:
			return cts.ReadRxcAndWriteSinkById(evt.Id, evt.Flags, evt.Data)
	}

	return nil
}
