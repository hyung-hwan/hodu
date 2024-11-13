package main

import "fmt"
import "net"
import "sync/atomic"
import "time"

type ServerPeerConn struct {
	route *ServerRoute
	conn_id uint32
	cts *ClientConn
	conn *net.TCPConn
	stop_req atomic.Bool
	client_peer_status_chan chan bool
	client_peer_opened_received atomic.Bool
	client_peer_closed_received atomic.Bool
}

func NewServerPeerConn(r *ServerRoute, c *net.TCPConn, id uint32) (*ServerPeerConn) {
	var spc ServerPeerConn

	spc.route = r
	spc.conn = c
	spc.conn_id = id
	spc.stop_req.Store(false)
	spc.client_peer_status_chan = make(chan bool, 16)
	spc.client_peer_opened_received.Store(false)
	spc.client_peer_closed_received.Store(false)

	return &spc
}

func (spc *ServerPeerConn) RunTask() error {
	var pss Hodu_PacketStreamServer
	var n int
	var buf [4096]byte
	var tmr *time.Timer
	var status bool
	var err error = nil

	pss = spc.route.cts.pss
//TODO: this needs to be guarded
	err = pss.Send(MakePeerStartedPacket(spc.route.id, spc.conn_id))
	if err != nil {
		// TODO: include route id and conn id in the error message
		err = fmt.Errorf("unable to send start-pts - %s\n", err.Error())
		goto done
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

			/*case <- spc->ctx->Done():
				tmr.Stop()
				goto done*/
		}
	}
	tmr.Stop()

	for {
		n, err = spc.conn.Read(buf[:])
		if err != nil {
			fmt.Printf("read error - %s\n", err.Error())
			break
		}

// TODO: this needs to be guarded
		err = pss.Send(MakePeerDataPacket(spc.route.id, spc.conn_id, buf[:n]))
		if err != nil {
			// TODO: include route id and conn id in the error message
			err = fmt.Errorf("unable to send data - %s\n", err.Error())
			goto done;
		}
	}

done:
	fmt.Printf("spc really ending..................\n")
	spc.ReqStop()
	spc.route.RemoveServerPeerConn(spc)
	//spc.cts.wg.Done()
	return err
}

func (spc *ServerPeerConn) ReqStop() {
	if spc.stop_req.CompareAndSwap(false, true) {
		var pss Hodu_PacketStreamServer
		var err error

		pss = spc.route.cts.pss

		if spc.client_peer_opened_received.CompareAndSwap(false, true) {
			spc.client_peer_status_chan <- false
		}
		spc.conn.Close()
		err = pss.Send(MakePeerStoppedPacket(spc.route.id, spc.conn_id))
		if err != nil {
			// TODO: print warning
			fmt.Printf ("WARNING - failed to report event to %s - %s\n", spc.route.cts.caddr, err.Error())
		}
	}
}

func (spc *ServerPeerConn) ReportEvent (event_type PACKET_KIND, event_data []byte) error {

	switch event_type {
		case PACKET_KIND_PEER_STARTED:
			if spc.client_peer_opened_received.CompareAndSwap(false, true) {
				spc.client_peer_status_chan <- true
			}

		case PACKET_KIND_PEER_STOPPED:
			if spc.client_peer_closed_received.CompareAndSwap(false, true) {
				spc.client_peer_status_chan <- false
			}

		case PACKET_KIND_PEER_DATA:
			var err error

			_, err = spc.conn.Write(event_data)
			if err != nil {
				// TODO: logging
				fmt.Printf ("WARNING - failed to write data from %s to %s\n", spc.route.cts.caddr, spc.conn.RemoteAddr().String())
			}

		default:
			// ignore all other events
			// TODO: produce warning in debug mode
	}
	return nil
}



