package hodu

import "errors"
import "io"
import "net"
import "strings"
import "sync"

func NewClientPeerConn(r *ClientRoute, c *net.TCPConn, id PeerId, pts_raddr string, pts_laddr string) *ClientPeerConn {
	var cpc ClientPeerConn

	cpc.route = r
	cpc.conn = c
	cpc.conn_id = id
	cpc.pts_raddr = pts_raddr
	cpc.pts_laddr = pts_laddr
	cpc.pts_eof.Store(false)
	cpc.stop_req.Store(false)

	return &cpc
}

func (cpc *ClientPeerConn) RunTask(wg *sync.WaitGroup) error {
	var err error
	var buf [4096]byte
	var n int

	defer wg.Done()

	for {
		n, err = cpc.conn.Read(buf[:])
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "use of closed network connection") { // i hate checking this condition with strings.Contains()
				cpc.route.cts.C.log.Write(cpc.route.cts.Sid, LOG_INFO,
					"Client-side peer(%d,%d,%s,%s) closed",
					cpc.route.Id, cpc.conn_id, cpc.conn.RemoteAddr().String(), cpc.conn.LocalAddr().String())
			} else {
				cpc.route.cts.C.log.Write(cpc.route.cts.Sid, LOG_ERROR,
					"Failed to read from client-side peer(%d,%d,%s,%s) - %s",
					cpc.route.Id, cpc.conn_id, cpc.conn.RemoteAddr().String(), cpc.conn.LocalAddr().String(), err.Error())
			}
			break
		}

		err = cpc.route.cts.psc.Send(MakePeerDataPacket(cpc.route.Id, cpc.conn_id, buf[0:n]))
		if err != nil {
			cpc.route.cts.C.log.Write(cpc.route.cts.Sid, LOG_ERROR,
				"Failed to write peer(%d,%d,%s,%s) data to server - %s",
				cpc.route.Id, cpc.conn_id, cpc.conn.RemoteAddr().String(), cpc.conn.LocalAddr().String(), err.Error())
			break
		}
	}

	cpc.route.cts.psc.Send(MakePeerStoppedPacket(cpc.route.Id, cpc.conn_id, cpc.conn.RemoteAddr().String(), cpc.conn.LocalAddr().String())) // nothing much to do upon failure. no error check here
	cpc.ReqStop()
	cpc.route.RemoveClientPeerConn(cpc)

	cpc.route.cts.C.ptc_mtx.Lock()
	cpc.route.cts.C.ptc_list.Remove(cpc.node_in_client)
	cpc.node_in_client = nil
	cpc.route.cts.C.ptc_mtx.Unlock()

	cpc.route.cts.ptc_mtx.Lock()
	cpc.route.cts.ptc_list.Remove(cpc.node_in_conn)
	cpc.node_in_conn = nil
	cpc.route.cts.ptc_mtx.Unlock()

	return nil
}

func (cpc *ClientPeerConn) ReqStop() {
	if cpc.stop_req.CompareAndSwap(false, true) {
		if cpc.conn != nil {
			cpc.conn.Close()
		}
	}
}

func (cpc *ClientPeerConn) CloseWrite() {
	if cpc.pts_eof.CompareAndSwap(false, true) {
		if cpc.conn != nil {
			cpc.conn.CloseWrite()
		}
	}
}
