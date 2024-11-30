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
	var err error
	var buf [4096]byte
	var n int

	defer wg.Done()

fmt.Printf("CONNECTION ESTABLISHED TO PEER... ABOUT TO READ DATA...\n")
	for {
		n, err = cpc.conn.Read(buf[:])
		if err != nil {
// TODO: add proper log header
			cpc.route.cts.cli.log.Write("", LOG_ERROR,  "Unable to read from the client-side peer %s - %s", cpc.conn.RemoteAddr().String(), err.Error())
			break
		}

		err = cpc.route.cts.psc.Send(MakePeerDataPacket(cpc.route.id, cpc.conn_id, buf[0:n]))
		if err != nil {
// TODO: add proper log header
			cpc.route.cts.cli.log.Write("", LOG_ERROR,  "Unable to write to server - %s", err.Error())
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
