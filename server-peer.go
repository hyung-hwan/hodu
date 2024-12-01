package hodu

import "errors"
import "fmt"
import "io"
import "net"
import "sync"
import "sync/atomic"
import "time"

type ServerPeerConn struct {
	route     *ServerRoute
	conn_id   uint32
	cts       *ClientConn
	conn      *net.TCPConn

	stop_chan chan bool
	stop_req  atomic.Bool

	client_peer_status_chan chan bool
	client_peer_started atomic.Bool
	client_peer_stopped atomic.Bool
	client_peer_eof atomic.Bool
}

func NewServerPeerConn(r *ServerRoute, c *net.TCPConn, id uint32) *ServerPeerConn {
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
fmt.Printf("~~~~~~~~~~~~~~~ NEW SERVER PEER CONNECTION ADDED %p\n", &spc)
	return &spc
}

func (spc *ServerPeerConn) RunTask(wg *sync.WaitGroup) {
	var pss *GuardedPacketStreamServer
	var n int
	var buf [4096]byte
	var tmr *time.Timer
	var status bool
	var err error = nil

	defer wg.Done()

	pss = spc.route.cts.pss
	err = pss.Send(MakePeerStartedPacket(spc.route.id, spc.conn_id, spc.conn.RemoteAddr().String(), spc.conn.LocalAddr().String()))
	if err != nil {
		// TODO: include route id and conn id in the error message
		fmt.Printf("unable to send start-pts - %s\n", err.Error())
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
				if pss.Send(MakePeerEofPacket(spc.route.id, spc.conn_id)) != nil {
					fmt.Printf("unable to report data - %s\n", err.Error())
					goto done
				}
				goto wait_for_stopped
			} else {
				fmt.Printf("read error - %s\n", err.Error())
				goto done
			}
		}

		err = pss.Send(MakePeerDataPacket(spc.route.id, spc.conn_id, buf[:n]))
		if err != nil {
			// TODO: include route id and conn id in the error message
			fmt.Printf("unable to send data - %s\n", err.Error())
			goto done
		}
	}

wait_for_stopped:
	for {
fmt.Printf ("******************* Waiting for peer Stop\n")
		select {
			case status = <-spc.client_peer_status_chan: // something not right... may use a different channel for closing...
				goto done
			case <-spc.stop_chan:
				goto done
		}
	}
fmt.Printf ("******************* Sending peer stopped\n")
	if pss.Send(MakePeerStoppedPacket(spc.route.id, spc.conn_id)) != nil {
		fmt.Printf("unable to report data - %s\n", err.Error())
		goto done
	}

done:
	if pss.Send(MakePeerStoppedPacket(spc.route.id, spc.conn_id)) != nil {
		fmt.Printf("unable to report data - %s\n", err.Error())
		// nothing much to do about the failure of sending this
	}

done_without_stop:
	fmt.Printf("SPC really ending..................\n")
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

func (spc *ServerPeerConn) ReportEvent(event_type PACKET_KIND, event_data []byte) error {

	switch event_type {
		case PACKET_KIND_PEER_STARTED:
fmt.Printf("******************* AAAAAAAAAAAAAAAAAAAaaa\n")
			if spc.client_peer_started.CompareAndSwap(false, true) {
				spc.client_peer_status_chan <- true
			}

		case PACKET_KIND_PEER_STOPPED:
fmt.Printf("******************* BBBBBBBBBBBBBBBBBBBBBBBB\n")
			// this event needs to close on the server-side peer connection.
			// sending false to the client_peer_status_chan isn't good enough to break
			// the Recv loop in RunTask().
			spc.ReqStop()

		case PACKET_KIND_PEER_EOF:
fmt.Printf("******************* BBBBBBBBBBBBBBBBBBBBBBBB CLIENT PEER EOF\n")
			// the client-side peer is not supposed to send data any more
			if spc.client_peer_eof.CompareAndSwap(false, true) {
				spc.conn.CloseWrite()
			}

		case PACKET_KIND_PEER_DATA:
fmt.Printf("******************* CCCCCCCCCCCCCCCCCCCCCCCccc\n")
			if spc.client_peer_eof.Load() == false {
				var err error

				_, err = spc.conn.Write(event_data)
				if err != nil {
					// TODO: logging
					fmt.Printf ("WARNING - failed to write data from %s to %s\n", spc.route.cts.caddr, spc.conn.RemoteAddr().String())
				}
			} else {
				// protocol error. the client must not relay more data from the client-side peer after EOF.
				fmt.Printf("WARNING - broken client - redundant data from %s to %s\n", spc.route.cts.caddr, spc.conn.RemoteAddr().String())
			}

		default:
			// ignore all other events
			// TODO: produce warning in debug mode
	}
	return nil
}
