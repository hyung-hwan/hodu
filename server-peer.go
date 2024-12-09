package hodu

import "errors"
import "io"
import "net"
import "sync"
import "sync/atomic"
import "time"

type ServerPeerConn struct {
	route     *ServerRoute
	conn_id   PeerId
	cts       *ClientConn
	conn      *net.TCPConn

	stop_chan chan bool
	stop_req  atomic.Bool

	client_peer_status_chan chan bool
	client_peer_started atomic.Bool
	client_peer_stopped atomic.Bool
	client_peer_eof atomic.Bool
}

func NewServerPeerConn(r *ServerRoute, c *net.TCPConn, id PeerId) *ServerPeerConn {
	var spc ServerPeerConn

	spc.route = r
	spc.conn = c
	spc.conn_id = id

	spc.stop_chan = make(chan bool, 8)
	spc.stop_req.Store(false)

	spc.client_peer_status_chan = make(chan bool, 8)
	spc.client_peer_started.Store(false)
	spc.client_peer_stopped.Store(false)
	spc.client_peer_eof.Store(false)
	return &spc
}

func (spc *ServerPeerConn) RunTask(wg *sync.WaitGroup) {
	var pss *GuardedPacketStreamServer
	var n int
	var buf [4096]byte
	var tmr *time.Timer
	var status bool
	var err error
	var conn_raddr string
	var conn_laddr string

	defer wg.Done()

	conn_raddr = spc.conn.RemoteAddr().String()
	conn_laddr = spc.conn.LocalAddr().String()

	pss = spc.route.cts.pss
	err = pss.Send(MakePeerStartedPacket(spc.route.id, spc.conn_id, conn_raddr, conn_laddr))
	if err != nil {
		spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
			"Failed to send peer_started event(%d,%d,%s,%s) to client - %s",
			spc.route.id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
		goto done_without_stop
	}

	tmr = time.NewTimer(2 * time.Second) // TODO: make this configurable...
wait_for_started:
	for {
		select {
			case status = <- spc.client_peer_status_chan:
				if status {
					break wait_for_started
				} else {
					// the socket must have been closed too.
					goto done
				}

			case <- tmr.C:
				// connection failure, not in time
				tmr.Stop()
				goto done

			case <-spc.stop_chan:
				tmr.Stop()
				goto done
		}
	}
	tmr.Stop()

	for {
		n, err = spc.conn.Read(buf[:])
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = pss.Send(MakePeerEofPacket(spc.route.id, spc.conn_id))
				if err != nil {
					spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
						"Failed to send peer_eof event(%d,%d,%s,%s) to client - %s",
						spc.route.id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
					goto done
				}
				goto wait_for_stopped
			} else {
				spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
					"Failed to read data from peer(%d,%d,%s,%s) - %s",
					spc.route.id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
				goto done
			}
		}

		err = pss.Send(MakePeerDataPacket(spc.route.id, spc.conn_id, buf[:n]))
		if err != nil {
			spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
					"Failed to send data from peer(%d,%d,%s,%s) to client - %s",
					spc.route.id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
			goto done
		}
	}

wait_for_stopped:
	for {
		select {
			case status = <-spc.client_peer_status_chan: // something not right... may use a different channel for closing...
				goto done
			case <-spc.stop_chan:
				goto done
		}
	}

done:
	err = pss.Send(MakePeerStoppedPacket(spc.route.id, spc.conn_id, spc.conn.RemoteAddr().String(), spc.conn.LocalAddr().String()))
	if err != nil {
		spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
			"Failed to send peer_stopped(%d,%d,%s,%s) to client - %s",
			spc.route.id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
		// nothing much to do about the failure of sending this
	}

done_without_stop:
	spc.ReqStop()
	spc.route.RemoveServerPeerConn(spc)
}

func (spc *ServerPeerConn) ReqStop() {
	if spc.stop_req.CompareAndSwap(false, true) {
		spc.stop_chan <- true

		if spc.client_peer_started.CompareAndSwap(false, true) {
			spc.client_peer_status_chan <- false
		}
		if spc.client_peer_stopped.CompareAndSwap(false, true) {
			spc.client_peer_status_chan <- false
		}

		spc.conn.Close() // to abort the main Recv() loop
	}
}

func (spc *ServerPeerConn) ReportEvent(event_type PACKET_KIND, event_data interface{}) error {

	switch event_type {
		case PACKET_KIND_PEER_STARTED:
			if spc.client_peer_started.CompareAndSwap(false, true) {
				spc.client_peer_status_chan <- true
			}

		case PACKET_KIND_PEER_ABORTED:
			spc.ReqStop()

		case PACKET_KIND_PEER_STOPPED:
			// this event needs to close on the server-side peer connection.
			// sending false to the client_peer_status_chan isn't good enough to break
			// the Recv loop in RunTask().
			spc.ReqStop()

		case PACKET_KIND_PEER_EOF:
			// the client-side peer is not supposed to send data any more
			if spc.client_peer_eof.CompareAndSwap(false, true) {
				spc.conn.CloseWrite()
			}

		case PACKET_KIND_PEER_DATA:
			if spc.client_peer_eof.Load() == false {
				var ok bool
				var data []byte

				data, ok = event_data.([]byte)
				if ok {
					var err error
					_, err = spc.conn.Write(data)
					if err != nil {
						spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
							"Failed to write data from %s to peer(%d,%d,%s) - %s",
							spc.route.cts.remote_addr, spc.route.id, spc.conn_id, spc.conn.RemoteAddr().String(), err.Error())
						spc.ReqStop()
					}
				} else {
					// this must not happen.
					spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
						"Protocol error - invalid data in peer_data event from %s to peer(%d,%d,%s)",
						spc.route.cts.remote_addr, spc.route.id, spc.conn_id, spc.conn.RemoteAddr().String())
					spc.ReqStop()
				}
			} else {
				// protocol error. the client must not relay more data from the client-side peer after EOF.
				spc.route.cts.svr.log.Write(spc.route.cts.sid, LOG_ERROR,
					"Protocol error - redundant data from %s to (%d,%d,%s)",
					spc.route.cts.remote_addr, spc.route.id, spc.conn_id, spc.conn.RemoteAddr().String())
				spc.ReqStop()
			}

		default:
			// ignore all other events
			// TODO: produce warning in debug mode
	}
	return nil
}
