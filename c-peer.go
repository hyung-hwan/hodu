package main

import "fmt"
import "net"

func NewClientPeerConn(r *ClientRoute, c net.Conn, id uint32) (*ClientPeerConn) {
	var cpc ClientPeerConn

	cpc.route = r
	cpc.conn = c
	cpc.conn_id = id
	cpc.stop_req.Store(false)
	//cpc.server_peer_status_chan = make(chan bool, 16)
	//cpc.server_peer_opened_received.Store(false)
	//cpc.server_peer_closed_received.Store(false)

	return &cpc
}

func (cpc *ClientPeerConn) RunTask() error {
	//var conn *net.TCPConn
	//var addr *net.TCPAddr
	var err error
	var buf [4096]byte
	var n int


	fmt.Printf("CONNECTION ESTABLISHED TO PEER... ABOUT TO READ DATA...\n")
	for {
		n, err = cpc.conn.Read(buf[:])
		if err != nil {
			fmt.Printf("unable to read from the client-side peer %s - %s\n", cpc.addr, err.Error())
			break
		}

// TODO: guarded call..
		err = cpc.route.cts.psc.Send(MakePeerDataPacket(cpc.route.id, cpc.conn_id, buf[0:n]))
		if err != nil {
			fmt.Printf("unable to write data to server - %s\n", err.Error())
			break
		}
	}

//done:
	cpc.ReqStop()
	//cpc.c.RemoveClientPeerConn(cpc)
	//cpc.c.wg.Done()
	return nil
}

func (cpc *ClientPeerConn) ReqStop() {
	// TODO: because of connect delay in Start, cpc.p may not be yet ready. handle this case...
	if cpc.stop_req.CompareAndSwap(false, true) {
		if cpc.conn != nil {
			cpc.conn.Close()
		}
	}
}
