package hodu

import "fmt"

// Rpx
func (cts *ServerConn) StartRpxWebById(srpx* ServerRpx, id uint64, data []byte) error {
	// pass the initial response to code in server-rpx.go
	srpx.start_chan <- data
	return nil
}

func (cts *ServerConn) StopRpxWebById(srpx* ServerRpx, id uint64) error {
	cts.S.log.Write(cts.Sid, LOG_DEBUG, "Requesting to stop rpx(%d)", srpx.id)
	srpx.ReqStop(true)
	cts.S.log.Write(cts.Sid, LOG_DEBUG, "Requested to stop rpx(%d)", srpx.id)
	return nil
}

func (cts *ServerConn) WriteRpxWebById(srpx* ServerRpx, id uint64, data []byte) error {
	var err error
	_, err = srpx.pw.Write(data)
	if err != nil {
		cts.S.log.Write(cts.Sid, LOG_ERROR, "Failed to write rpx data(%d) to rpx pipe - %s", id, err.Error())
		srpx.ReqStop(true)
	}
	return err
}

func (cts *ServerConn) EofRpxWebById(srpx* ServerRpx, id uint64) error {
	srpx.ReqStop(false)
	return nil
}

func (cts *ServerConn) HandleRpxEvent(packet_type PACKET_KIND, evt *RpxEvent) error {
	var ok bool
	var rpx* ServerRpx

	cts.rpx_mtx.Lock()
	rpx, ok = cts.rpx_map[evt.Id]
	if !ok {
		cts.rpx_mtx.Unlock()
		return fmt.Errorf("unknown rpx id - %v", evt.Id)
	}
	cts.rpx_mtx.Unlock()

	switch packet_type {
		case PACKET_KIND_RPX_START:
			return cts.StartRpxWebById(rpx, evt.Id, evt.Data)

		case PACKET_KIND_RPX_STOP:
			// stop requested from the server
			return cts.StopRpxWebById(rpx, evt.Id)

		case PACKET_KIND_RPX_EOF:
			return cts.EofRpxWebById(rpx, evt.Id)

		case PACKET_KIND_RPX_DATA:
			return cts.WriteRpxWebById(rpx, evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}
