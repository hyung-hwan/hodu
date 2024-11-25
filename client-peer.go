package hodu

import "fmt"
import "net"
import "sync"

func NewClientPeerConn(r *ClientRoute, c *net.TCPConn, id uint32) *ClientPeerConn {
	var cpc ClientPeerConn

	cpc.route = r
	cpc.conn = c
	cpc.conn_id = id
	cpc.stop_req.Store(false)
	cpc.server_peer_eof.Store(false)

	return &cpc
}

func (cpc *ClientPeerConn) RunTask(wg *sync.WaitGroup) error {
	//var conn *net.TCPConn
	//var addr *net.TCPAddr
	var err error
	var buf [4096]byte
	var n int

	defer wg.Done()

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

	cpc.route.cts.psc.Send(MakePeerStoppedPacket(cpc.route.id, cpc.conn_id)) // nothing much to do upon failure. no error check here

	cpc.ReqStop()
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

func (cpc *ClientPeerConn) CloseWrite() {
	if cpc.server_peer_eof.CompareAndSwap(false, true) {
		if cpc.conn != nil {
			cpc.conn.CloseWrite()
		}
	}
}
