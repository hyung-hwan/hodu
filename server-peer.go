package hodu

import "context"
import "errors"
import "io"
import "net"
import "strings"
import "sync"
import "sync/atomic"
import "time"

type ServerPeerConn struct {
	route     *ServerRoute
	conn_id   PeerId
	conn      *net.TCPConn

	stop_chan chan bool
	stop_req  atomic.Bool

	client_peer_status_chan chan bool
	client_peer_started atomic.Bool
	client_peer_stopped atomic.Bool
	client_peer_eof atomic.Bool
	client_peer_laddr string
	client_peer_raddr string
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
	var waitctx context.Context
	var cancel_wait context.CancelFunc
	var status bool
	var err error
	var conn_raddr string
	var conn_laddr string

	defer wg.Done()

	conn_raddr = spc.conn.RemoteAddr().String()
	conn_laddr = spc.conn.LocalAddr().String()

	pss = spc.route.Cts.pss
	err = pss.Send(MakePeerStartedPacket(spc.route.Id, spc.conn_id, conn_raddr, conn_laddr))
	if err != nil {
		spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
			"Failed to send peer_started event(%d,%d,%s,%s) to client - %s",
			spc.route.Id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
		goto done_without_stop
	}

	// set up a timer to set waiting duration until the connection is
	// actually established on the client side and it's informed...
	waitctx, cancel_wait = context.WithTimeout(spc.route.Cts.S.Ctx, 5 * time.Second) // TODO: make this configurable
wait_for_started:
	for {
		select {
			case status = <- spc.client_peer_status_chan:
				if !status {
					// the socket must have been closed too.
					cancel_wait()
					goto done
				}
				break wait_for_started

			case <- waitctx.Done():
				cancel_wait()
				goto done

			case <-spc.stop_chan:
				cancel_wait()
				goto done
		}
	}
	cancel_wait()

	for {
		n, err = spc.conn.Read(buf[:])
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") { // i don't like this way to check this error.
				err = pss.Send(MakePeerEofPacket(spc.route.Id, spc.conn_id))
				if err != nil {
					spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
						"Failed to send peer_eof event(%d,%d,%s,%s) to client - %s",
						spc.route.Id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
					goto done
				}
				goto wait_for_stopped
			} else {
				spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
					"Failed to read data from peer(%d,%d,%s,%s) - %s",
					spc.route.Id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
				goto done
			}
		}

		err = pss.Send(MakePeerDataPacket(spc.route.Id, spc.conn_id, buf[:n]))
		if err != nil {
			spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
					"Failed to send data from peer(%d,%d,%s,%s) to client - %s",
					spc.route.Id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
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
	err = pss.Send(MakePeerStoppedPacket(spc.route.Id, spc.conn_id, spc.conn.RemoteAddr().String(), spc.conn.LocalAddr().String()))
	if err != nil {
		spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
			"Failed to send peer_stopped(%d,%d,%s,%s) to client - %s",
			spc.route.Id, spc.conn_id, conn_raddr, conn_laddr, err.Error())
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
			var ok bool
			var pd *PeerDesc

			pd, ok = event_data.(*PeerDesc)
			if !ok {
				// something wrong. leave it unknown.
				spc.client_peer_laddr = "";
				spc.client_peer_raddr = "";
			} else {
				spc.client_peer_laddr = pd.LocalAddrStr
				spc.client_peer_raddr = pd.RemoteAddrStr
			}

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
						spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
							"Failed to write data from %s to peer(%d,%d,%s) - %s",
							spc.route.Cts.RemoteAddr, spc.route.Id, spc.conn_id, spc.conn.RemoteAddr().String(), err.Error())
						spc.ReqStop()
					}
				} else {
					// this must not happen.
					spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
						"Protocol error - invalid data in peer_data event from %s to peer(%d,%d,%s)",
						spc.route.Cts.RemoteAddr, spc.route.Id, spc.conn_id, spc.conn.RemoteAddr().String())
					spc.ReqStop()
				}
			} else {
				// protocol error. the client must not relay more data from the client-side peer after EOF.
				spc.route.Cts.S.log.Write(spc.route.Cts.Sid, LOG_ERROR,
					"Protocol error - redundant data from %s to (%d,%d,%s)",
					spc.route.Cts.RemoteAddr, spc.route.Id, spc.conn_id, spc.conn.RemoteAddr().String())
				spc.ReqStop()
			}

		default:
			// ignore all other events
			// TODO: produce warning in debug mode
	}
	return nil
}
