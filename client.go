package hodu

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "math/rand"
import "net"
import "net/http"
import "sync"
import "sync/atomic"
import "time"

import "google.golang.org/grpc"
import "google.golang.org/grpc/codes"
import "google.golang.org/grpc/credentials/insecure"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/status"

type PacketStreamClient grpc.BidiStreamingClient[Packet, Packet]

type ClientConnMapByAddr = map[string]*ClientConn
type ClientConnMap = map[uint32]*ClientConn
type ClientPeerConnMap = map[uint32]*ClientPeerConn
type ClientRouteMap = map[uint32]*ClientRoute
type ClientPeerCancelFuncMap = map[uint32]context.CancelFunc

// --------------------------------------------------------------------
type ClientConfig struct {
	ServerAddr string
	PeerAddrs []string
}

type ClientConfigActive struct {
	Id uint32
	ClientConfig
}

type Client struct {
	ctx         context.Context
	ctx_cancel  context.CancelFunc
	tlscfg     *tls.Config
	ctl_prefix  string

	ext_mtx     sync.Mutex
	ext_svcs   []Service
	ctl_mux    *http.ServeMux
	ctl        *http.Server // control server

	cts_mtx     sync.Mutex
	cts_map_by_addr ClientConnMapByAddr
	cts_map     ClientConnMap

	wg          sync.WaitGroup
	stop_req    atomic.Bool
	stop_chan   chan bool

	log         Logger
}

// client connection to server
type ClientConn struct {
	cli        *Client
	cfg         ClientConfigActive
	id          uint32
	lid         string

	local_addr  string
	remote_addr string
	conn       *grpc.ClientConn // grpc connection to the server
	hdc         HoduClient
	psc        *GuardedPacketStreamClient // guarded grpc stream

	s_seed      Seed
	c_seed      Seed

	route_mtx   sync.Mutex
	route_map   ClientRouteMap
	route_wg    sync.WaitGroup

	stop_req    atomic.Bool
	stop_chan   chan bool
}

type ClientRoute struct {
	cts *ClientConn
	id uint32
	peer_addr string
	server_peer_listen_addr *net.TCPAddr
	proto ROUTE_PROTO

	ptc_mtx        sync.Mutex
	ptc_map        ClientPeerConnMap
	ptc_cancel_map ClientPeerCancelFuncMap
	ptc_wg sync.WaitGroup

	stop_req atomic.Bool
	stop_chan chan bool
}

type ClientPeerConn struct {
	route *ClientRoute
	conn_id uint32
	conn *net.TCPConn

	pts_laddr string // server-local addreess of the server-side peer
	pts_raddr string // address of the server-side peer
	pts_eof atomic.Bool

	stop_chan chan bool
	stop_req atomic.Bool
}

type GuardedPacketStreamClient struct {
	mtx sync.Mutex
	//psc Hodu_PacketStreamClient
	Hodu_PacketStreamClient
}

// ------------------------------------

func (g *GuardedPacketStreamClient) Send(data *Packet) error {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	//return g.psc.Send(data)
	return g.Hodu_PacketStreamClient.Send(data)
}

/*func (g *GuardedPacketStreamClient) Recv() (*Packet, error) {
	return g.psc.Recv()
}

func (g *GuardedPacketStreamClient) Context() context.Context {
	return g.psc.Context()
}*/

// --------------------------------------------------------------------
func NewClientRoute(cts *ClientConn, id uint32, addr string, proto ROUTE_PROTO) *ClientRoute {
	var r ClientRoute

	r.cts = cts
	r.id = id
	r.ptc_map = make(ClientPeerConnMap)
	r.ptc_cancel_map = make(ClientPeerCancelFuncMap)
	r.proto = proto
	r.peer_addr = addr
	r.stop_req.Store(false)
	r.stop_chan = make(chan bool, 8)

	return &r
}

func (r *ClientRoute) AddNewClientPeerConn(c *net.TCPConn, pts_id uint32, pts_raddr string, pts_laddr string) (*ClientPeerConn, error) {
	var ptc *ClientPeerConn

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

	ptc = NewClientPeerConn(r, c, pts_id, pts_raddr, pts_laddr)
	r.ptc_map[ptc.conn_id] = ptc

	return ptc, nil
}

func (r *ClientRoute) RemoveClientPeerConn(ptc *ClientPeerConn) error {
	var c *ClientPeerConn
	var ok bool

	r.ptc_mtx.Lock()
	c, ok = r.ptc_map[ptc.conn_id]
	if !ok {
		r.ptc_mtx.Unlock()
		return fmt.Errorf("non-existent peer id - %d", ptc.conn_id)
	}
	if c != ptc {
		r.ptc_mtx.Unlock()
		return fmt.Errorf("non-existent peer id - %d", ptc.conn_id)
	}
	delete(r.ptc_map, ptc.conn_id)
	r.ptc_mtx.Unlock()

	ptc.ReqStop()
	return nil
}

func (r *ClientRoute) RemoveAllClientPeerConns() {
	var c *ClientPeerConn

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

	for _, c = range r.ptc_map {
		delete(r.ptc_map, c.conn_id)
		c.ReqStop()
	}
}

func (r *ClientRoute) ReqStopAllClientPeerConns() {
	var c *ClientPeerConn

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

	for _, c = range r.ptc_map {
		c.ReqStop()
	}
}

func (r *ClientRoute) FindClientPeerConnById(conn_id uint32) *ClientPeerConn {
	var c *ClientPeerConn
	var ok bool

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

	c, ok = r.ptc_map[conn_id]
	if !ok {
		return nil
	}

	return c
}

func (r *ClientRoute) RunTask(wg *sync.WaitGroup) {
	var err error

	// this task on the route object isn't actually necessary.
	// most useful works are triggered by ReportEvent() and done by ConnectToPeer()
	defer wg.Done()

	r.cts.cli.log.Write("", LOG_DEBUG, "Sending route-start for id=%d peer=%s to %s", r.id, r.peer_addr, r.cts.cfg.ServerAddr)
	err = r.cts.psc.Send(MakeRouteStartPacket(r.id, r.proto, r.peer_addr))
	if err != nil {
		r.cts.cli.log.Write("", LOG_DEBUG, "Failed to Send route-start for id=%d peer=%s to %s", r.id, r.peer_addr, r.cts.cfg.ServerAddr)
		goto done
	}

main_loop:
	for {
		select {
			case <-r.stop_chan:
				break main_loop
		}
	}

done:
	r.ReqStop()
	r.ptc_wg.Wait() // wait for all peer tasks are finished

	r.cts.cli.log.Write("", LOG_DEBUG, "Sending route-stop for id=%d peer=%s to %s", r.id, r.peer_addr, r.cts.cfg.ServerAddr)
	r.cts.psc.Send(MakeRouteStopPacket(r.id, r.proto, r.peer_addr))
	r.cts.RemoveClientRoute(r)
fmt.Printf("*** End fo Client Route Task\n")
}

func (r *ClientRoute) ReqStop() {
	if r.stop_req.CompareAndSwap(false, true) {
		var ptc *ClientPeerConn
		for _, ptc = range r.ptc_map {
			ptc.ReqStop()
		}
		r.stop_chan <- true
	}
fmt.Printf("*** Sent stop request to Route..\n")
}

func (r *ClientRoute) ConnectToPeer(pts_id uint32, pts_raddr string, pts_laddr string, wg *sync.WaitGroup) {
	var err error
	var conn net.Conn
	var real_conn *net.TCPConn
	var ptc *ClientPeerConn
	var d net.Dialer
	var ctx context.Context
	var cancel context.CancelFunc
	var ok bool

	defer wg.Done()

// TODO: make timeout value configurable
// TODO: fire the cancellation function upon stop request???
	ctx, cancel = context.WithTimeout(r.cts.cli.ctx, 10 * time.Second)
	r.ptc_mtx.Lock()
	r.ptc_cancel_map[pts_id] = cancel
	r.ptc_mtx.Unlock()

	d.LocalAddr = nil // TOOD: use this if local address is specified
	conn, err = d.DialContext(ctx, "tcp", r.peer_addr)

	r.ptc_mtx.Lock()
	delete(r.ptc_cancel_map, pts_id)
	r.ptc_mtx.Unlock()

	if err != nil {
// TODO: make send peer started failure mesage?
		fmt.Printf("failed to connect to %s - %s\n", r.peer_addr, err.Error())
		goto peer_aborted
	}

	real_conn, ok = conn.(*net.TCPConn)
	if !ok {
		fmt.Printf("not tcp connection - %s\n", err.Error())
		goto peer_aborted
	}

	ptc, err = r.AddNewClientPeerConn(real_conn, pts_id, pts_raddr, pts_laddr)
	if err != nil {
		// TODO: logging
// TODO: make send peer started failure mesage?
		fmt.Printf("YYYYYYYY - %s\n", err.Error())
		goto peer_aborted
	}
	fmt.Printf("STARTED NEW SERVER PEER STAK\n")
	err = r.cts.psc.Send(MakePeerStartedPacket(r.id, ptc.conn_id, real_conn.RemoteAddr().String(), real_conn.LocalAddr().String()))
	if err != nil {
		fmt.Printf("CLOSING NEW SERVER PEER STAK - %s\n", err.Error())
		goto peer_aborted
	}

	wg.Add(1)
	go ptc.RunTask(wg)
	return

peer_aborted:
	if conn != nil {
		conn.Close()
		err = r.cts.psc.Send(MakePeerAbortedPacket(r.id, ptc.conn_id))
		if err != nil {
			// TODO: logging
		}
	}
}

func (r *ClientRoute) DisconnectFromPeer(pts_id uint32) error {
	var ptc *ClientPeerConn
	var cancel context.CancelFunc
	var ok bool

	r.ptc_mtx.Lock()
	cancel, ok = r.ptc_cancel_map[pts_id]
	if ok {
fmt.Printf("~~~~~~~~~~~~~~~~ cancelling.....\n")
		cancel()
	}

	ptc, ok = r.ptc_map[pts_id]
	if !ok {
		r.ptc_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %u", pts_id)
	}
	r.ptc_mtx.Unlock()

	ptc.ReqStop()
	return nil
}

func (r *ClientRoute) CloseWriteToPeer(pts_id uint32) error {
	var ptc *ClientPeerConn
	var ok bool

	r.ptc_mtx.Lock()
	ptc, ok = r.ptc_map[pts_id]
	if !ok {
		r.ptc_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %u", pts_id)
	}
	r.ptc_mtx.Unlock()

	ptc.CloseWrite()
	return nil
}

func (r *ClientRoute) ReportEvent(pts_id uint32, event_type PACKET_KIND, event_data interface{}) error {
	var err error

	switch event_type {
		case PACKET_KIND_ROUTE_STARTED:
			var ok bool
			var str string
			str, ok = event_data.(string)
			if !ok {
				// TODO: internal error
fmt.Printf("INTERNAL ERROR. MUST NOT HAPPEN\n")
			} else {
				var addr *net.TCPAddr
				addr, err = net.ResolveTCPAddr("tcp", str)
				if err != nil {
					// TODO:
				} else {
					r.server_peer_listen_addr = addr
				}
			}

		case PACKET_KIND_ROUTE_STOPPED:
			// TODO:

		case PACKET_KIND_PEER_STARTED:
fmt.Printf("GOT PEER STARTD . CONENCT TO CLIENT_SIDE PEER\n")
			var ok bool
			var pd *PeerDesc

			pd, ok = event_data.(*PeerDesc)
			if !ok {
				// TODO: internal error
fmt.Printf("INTERNAL ERROR. MUST NOT HAPPEN\n")
			} else {
				r.ptc_wg.Add(1)
				go r.ConnectToPeer(pts_id, pd.RemoteAddrStr, pd.LocalAddrStr, &r.ptc_wg)
			}

		case PACKET_KIND_PEER_ABORTED:
			fallthrough
		case PACKET_KIND_PEER_STOPPED:
fmt.Printf("GOT PEER STOPPED . DISCONNECTION FROM CLIENT_SIDE PEER\n")
			err = r.DisconnectFromPeer(pts_id)
			if err != nil {
				// TODO:
			}

		case PACKET_KIND_PEER_EOF:
fmt.Printf("GOT PEER EOF. REMEMBER EOF\n")
			err = r.CloseWriteToPeer(pts_id)
			if err != nil {
				// TODO:
			}

		case PACKET_KIND_PEER_DATA:
			var ptc *ClientPeerConn
			var ok bool
			var err error
			var data []byte

			ptc, ok = r.ptc_map[pts_id]
			if ok {
				data, ok = event_data.([]byte)
				if ok {
					_, err = ptc.conn.Write(data)
					return err
				} else {
					// internal error
				}
			} else {

			}

		// TODO: other types
	}

	return nil
}

// --------------------------------------------------------------------
func NewClientConn(c *Client, cfg *ClientConfig) *ClientConn {
	var cts ClientConn

	cts.cli = c
	cts.route_map = make(ClientRouteMap)
	cts.cfg.ClientConfig = *cfg
	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 8)

	// the actual connection to the server is established in the main task function
	// The cts.conn, cts.hdc, cts.psc fields are left unassigned here.

	return &cts
}

func (cts *ClientConn) AddNewClientRoute(addr string, proto ROUTE_PROTO) (*ClientRoute, error) {
	var r *ClientRoute
	var id uint32
	var ok bool

	cts.route_mtx.Lock()

	id = rand.Uint32()
	for {
		_, ok = cts.route_map[id]
		if !ok { break }
		id++
	}

	//if cts.route_map[route_id] != nil {
	//	cts.route_mtx.Unlock()
	//	return nil, fmt.Errorf("existent route id - %d", route_id)
	//}
	r = NewClientRoute(cts, id, addr, proto)
	cts.route_map[id] = r
	cts.route_mtx.Unlock()

fmt.Printf("added client route.... %d -> %d\n", id, len(cts.route_map))
	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ClientConn) ReqStopAllClientRoutes() {
	var r *ClientRoute

	cts.route_mtx.Lock()
	defer cts.route_mtx.Unlock()

	for _, r = range cts.route_map {
		r.ReqStop()
	}
}

func (cts *ClientConn) RemoveAllClientRoutes() {
	var r *ClientRoute

	cts.route_mtx.Lock()
	defer cts.route_mtx.Unlock()

	for _, r = range cts.route_map {
		delete(cts.route_map, r.id)
		r.ReqStop()
	}
}

func (cts *ClientConn) RemoveClientRoute(route *ClientRoute) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route.id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route.id)
	}
	if r != route {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route.id)
	}
	delete(cts.route_map, route.id)
	cts.route_mtx.Unlock()

	r.ReqStop()
	return nil
}

func (cts *ClientConn) RemoveClientRouteById(route_id uint32) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route_id)
	}
	delete(cts.route_map, route_id)
	cts.route_mtx.Unlock()

	r.ReqStop()
	return nil
}

func (cts *ClientConn) FindClientRouteById(route_id uint32) *ClientRoute {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return nil
	}
	cts.route_mtx.Unlock()

	return r
}

func (cts *ClientConn) AddClientRoutes(peer_addrs []string) error {
	var v string
	var err error

	for _, v = range peer_addrs {
		_, err = cts.AddNewClientRoute(v, ROUTE_PROTO_TCP)
		if err != nil {
			return fmt.Errorf("unable to add client route for %s - %s", v, err.Error())
		}
	}

	return nil
}

func (cts *ClientConn) disconnect_from_server() {
	if cts.conn != nil {
		var r *ClientRoute

		cts.route_mtx.Lock()
		for _, r = range cts.route_map {
			r.ReqStop()
		}
		cts.route_mtx.Unlock()

		cts.conn.Close()
		// don't reset cts.conn to nil here
		// if this function is called from RunTask()
		// for reconnection, it will be set to a new value
		// immediately after the start_over lable in it.
		// if it's called from ReqStop(), we don't really
		// need to care about it.
	}
}

func (cts *ClientConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		cts.disconnect_from_server()
		cts.stop_chan <- true
	}
}

func (cts *ClientConn) RunTask(wg *sync.WaitGroup) {
	var psc PacketStreamClient
	var slpctx context.Context
	var c_seed Seed
	var s_seed *Seed
	var p *peer.Peer
	var ok bool
	var err error

	defer wg.Done() // arrange to call at the end of this function

start_over:
	cts.cli.log.Write(cts.lid, LOG_INFO, "Connecting to server %s", cts.cfg.ServerAddr)
	cts.conn, err = grpc.NewClient(cts.cfg.ServerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		cts.cli.log.Write(cts.lid, LOG_ERROR, "Failed to make client to server %s - %s", cts.cfg.ServerAddr, err.Error())
		goto reconnect_to_server
	}
	cts.hdc = NewHoduClient(cts.conn)

// TODO: HANDLE connection timeout.. may have to run GetSeed or PacketStream in anther goroutnine
//	ctx, _/*cancel*/ := context.WithTimeout(context.Background(), time.Second)

	// seed exchange is for furture expansion of the protocol
	// there is nothing to do much about it for now.
	c_seed.Version = HODU_VERSION
	c_seed.Flags = 0
	s_seed, err = cts.hdc.GetSeed(cts.cli.ctx, &c_seed)
	if err != nil {
		cts.cli.log.Write(cts.lid, LOG_ERROR, "Failed to get seed from server %s - %s", cts.cfg.ServerAddr, err.Error())
		goto reconnect_to_server
	}
	cts.s_seed = *s_seed
	cts.c_seed = c_seed

	cts.cli.log.Write(cts.lid, LOG_INFO, "Got seed from server %s - ver=%#x", cts.cfg.ServerAddr, cts.s_seed.Version)

	psc, err = cts.hdc.PacketStream(cts.cli.ctx)
	if err != nil {
		cts.cli.log.Write(cts.lid, LOG_ERROR, "Failed to get packet stream from server %s - %s", cts.cfg.ServerAddr, err.Error())
		goto reconnect_to_server
	}

	p, ok = peer.FromContext(psc.Context())
	if ok {
		cts.remote_addr = p.Addr.String()
		cts.local_addr = p.LocalAddr.String()
	}

	cts.cli.log.Write(cts.lid, LOG_INFO, "Got packet stream from server %s", cts.cfg.ServerAddr)

	cts.psc = &GuardedPacketStreamClient{Hodu_PacketStreamClient: psc}

	// the connection structure to a server is ready.
	// let's add routes to the client-side peers.
	err = cts.AddClientRoutes(cts.cfg.PeerAddrs)
	if err != nil {
		cts.cli.log.Write(cts.lid, LOG_INFO, "Failed to add routes to server %s for %v - %s", cts.cfg.ServerAddr, cts.cfg.PeerAddrs, err.Error())
		goto done
	}

fmt.Printf("[%v]\n", cts.route_map)

	for {
		var pkt *Packet

		select {
			case <-cts.cli.ctx.Done():
fmt.Printf("context doine... error - %s\n", cts.cli.ctx.Err().Error())
				goto done

			case <-cts.stop_chan:
				goto done

			default:
				// no other case is ready. run the code below select.
				// without the default case, the select construct would block
		}

		pkt, err = psc.Recv()
		if err != nil {
			if status.Code(err) == codes.Canceled || errors.Is(err, net.ErrClosed) {
				goto reconnect_to_server
			} else {
				cts.cli.log.Write(cts.lid, LOG_INFO, "Failed to receive packet form server %s - %s", cts.cfg.ServerAddr, err.Error())
				goto reconnect_to_server
			}
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_STARTED:
				// the server side managed to set up the route the client requested
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
	fmt.Printf ("SERVER LISTENING ON %s\n", x.Route.AddrStr)
					err = cts.ReportEvent(x.Route.RouteId, 0, pkt.Kind, x.Route.AddrStr)
					if err != nil {
						// TODO:
					} else {
						// TODO:
					}
				} else {
					// TODO: send invalid request... or simply keep quiet?
				}

			case PACKET_KIND_ROUTE_STOPPED:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					err = cts.ReportEvent(x.Route.RouteId, 0, pkt.Kind, nil)
					if err != nil {
						// TODO:
					} else {
						// TODO:
					}
				} else {
					// TODO: send invalid request... or simply keep quiet?
				}

			case PACKET_KIND_PEER_STARTED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STARTED, x.Peer)
					if err != nil {
						// TODO:
					} else {
						// TODO:
					}
				} else {
					// TODO
				}

			case PACKET_KIND_PEER_STOPPED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STOPPED, nil)
					if err != nil {
						// TODO:
					} else {
						// TODO:
					}
				} else {
					// TODO
				}

			case PACKET_KIND_PEER_EOF:
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_EOF, nil)
					if err != nil {
						// TODO:
					} else {
						// TODO:
					}
				} else {
					// TODO
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
	//fmt.Printf ("**** GOT PEER DATA\n")
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(x.Data.RouteId, x.Data.PeerId, PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
		fmt.Printf("failed to report event - %s\n", err.Error())
						// TODO:
					} else {
						// TODO:
					}
				} else {
					// TODO
				}
		}
	}

done:
	cts.cli.log.Write("", LOG_INFO, "Disconnected from server %s", cts.cfg.ServerAddr)
	//cts.RemoveClientRoutes() // this isn't needed as each task removes itself from cts upon its termination
	cts.ReqStop()
wait_for_termination:
	cts.route_wg.Wait() // wait until all route tasks are finished
	cts.cli.RemoveClientConn(cts)
	return

reconnect_to_server:
	cts.disconnect_from_server()

	// wait for 2 seconds
	slpctx, _ = context.WithTimeout(cts.cli.ctx, 2 * time.Second)
	select {
		case <-cts.cli.ctx.Done():
			fmt.Printf("context doine... error - %s\n", cts.cli.ctx.Err().Error())
			goto done
		case <-cts.stop_chan:
			// this signal indicates that ReqStop() has been called
			// so jumt to the waiting label
			goto wait_for_termination
		case <-slpctx.Done():
			// do nothing
	}
	goto start_over // and reconnect
}

func (cts *ClientConn) ReportEvent (route_id uint32, pts_id uint32, event_type PACKET_KIND, event_data interface{}) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route id - %d", route_id)
	}
	cts.route_mtx.Unlock()

	return r.ReportEvent(pts_id, event_type, event_data)
}

// --------------------------------------------------------------------

func NewClient(ctx context.Context, ctl_addr string, logger Logger, tlscfg *tls.Config) *Client {
	var c Client

	c.ctx, c.ctx_cancel = context.WithCancel(ctx)
	c.tlscfg = tlscfg
	c.ext_svcs = make([]Service, 0, 1)
	c.cts_map_by_addr = make(ClientConnMapByAddr)
	c.cts_map = make(ClientConnMap)
	c.stop_req.Store(false)
	c.stop_chan = make(chan bool, 8)
	c.log = logger
	c.ctl_prefix = "" // TODO:

	c.ctl_mux = http.NewServeMux()
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns", &client_ctl_client_conns{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}", &client_ctl_client_conns_id{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes", &client_ctl_client_conns_id_routes{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes/{route_id}", &client_ctl_client_conns_id_routes_id{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes/{route_id}/peers", &client_ctl_client_conns_id_routes_id_peers{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes/{route_id}/peers/{peer_id}", &client_ctl_client_conns_id_routes_id_peers_id{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/server-conns", &client_ctl_clients{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/server-conns/{id}", &client_ctl_clients_id{c: &c})

	c.ctl = &http.Server{
		Addr: ctl_addr,
		Handler: c.ctl_mux,
		// TODO: more settings
	}

	return &c
}

func (c *Client) AddNewClientConn(cfg *ClientConfig) (*ClientConn, error) {
	var cts *ClientConn
	var ok bool
	var id uint32

	cts = NewClientConn(c, cfg)

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	_, ok = c.cts_map_by_addr[cfg.ServerAddr]
	if ok {
		return nil, fmt.Errorf("existing server - %s", cfg.ServerAddr)
	}

	id = rand.Uint32()
	for {
		_, ok = c.cts_map[id]
		if !ok { break }
		id++
	}
	cts.id = id
	cts.cfg.Id = id // store it again in the active configuration for easy access via control channel
	cts.lid = fmt.Sprintf("%d", id) // id in string used for logging

	c.cts_map_by_addr[cfg.ServerAddr] = cts
	c.cts_map[id] = cts
fmt.Printf("ADD total servers %d\n", len(c.cts_map_by_addr))
	return cts, nil
}

func (c* Client) ReqStopAllClientConns() {
	var cts *ClientConn

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	for _, cts = range c.cts_map {
		cts.ReqStop()
	}
}

func (c *Client) RemoveAllClientConns() {
	var cts *ClientConn

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	for _, cts = range c.cts_map {
		delete(c.cts_map_by_addr, cts.cfg.ServerAddr)
		delete(c.cts_map, cts.id)
		cts.ReqStop()
	}
}

func (c *Client) RemoveClientConn(cts *ClientConn) error {
	var conn *ClientConn
	var ok bool

	c.cts_mtx.Lock()

	conn, ok = c.cts_map[cts.id]
	if !ok {
		c.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.id)
	}
	if conn != cts {
		c.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.id)
	}

	delete(c.cts_map_by_addr, cts.cfg.ServerAddr)
	delete(c.cts_map, cts.id)
fmt.Printf("REMOVEDDDDDD CONNECTION FROM %s total servers %d\n", cts.cfg.ServerAddr, len(c.cts_map_by_addr))
	c.cts_mtx.Unlock()

	c.ReqStop()
	return nil
}

func (c *Client) RemoveClientConnById(conn_id uint32) error {
	var cts *ClientConn
	var ok bool

	c.cts_mtx.Lock()

	cts, ok = c.cts_map[conn_id]
	if !ok {
		c.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", conn_id)
	}

	// NOTE: removal by id doesn't perform identity check

	delete(c.cts_map_by_addr, cts.cfg.ServerAddr)
	delete(c.cts_map, cts.id)
fmt.Printf("REMOVEDDDDDD CONNECTION FROM %s total servers %d\n", cts.cfg.ServerAddr, len(c.cts_map_by_addr))
	c.cts_mtx.Unlock()

	cts.ReqStop()
	return nil
}

func (c *Client) FindClientConnById(id uint32) *ClientConn {
	var cts *ClientConn
	var ok bool

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	cts, ok = c.cts_map[id]
	if !ok {
		return nil
	}

	return cts
}

func (c *Client) FindClientRouteById(conn_id uint32, route_id uint32) *ClientRoute {
	var cts *ClientConn
	var ok bool

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	cts, ok = c.cts_map[conn_id]
	if !ok {
		return nil
	}

	return cts.FindClientRouteById(route_id)
}

func (c *Client) FindClientPeerConnById(conn_id uint32, route_id uint32, peer_id uint32) *ClientPeerConn {
	var cts *ClientConn
	var r* ClientRoute
	var ok bool

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	cts, ok = c.cts_map[conn_id]
	if !ok {
		return nil
	}

	cts.route_mtx.Lock()
	defer cts.route_mtx.Unlock()

	r, ok = cts.route_map[route_id]
	if !ok {
		return nil
	}

	return r.FindClientPeerConnById(peer_id)
}

func (c *Client) ReqStop() {
	if c.stop_req.CompareAndSwap(false, true) {
		var cts *ClientConn

		if c.ctl != nil {
			c.ctl.Shutdown(c.ctx) // to break c.ctl.ListenAndServe()
		}

		for _, cts = range c.cts_map {
			cts.ReqStop()
		}

		c.stop_chan <- true
		c.ctx_cancel()
	}
}

func (c *Client) RunCtlTask(wg *sync.WaitGroup) {
	var err error

	defer wg.Done()

	err = c.ctl.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		c.log.Write("", LOG_DEBUG, "Control channel closed")
	} else {
		c.log.Write("", LOG_ERROR, "Control channel error - %s", err.Error())
	}
}

func (c *Client) StartCtlService() {
	c.wg.Add(1)
	go c.RunCtlTask(&c.wg)
}


func (c *Client) RunTask(wg *sync.WaitGroup) {
	// just a place holder to pacify the Service interface
	// StartService() calls cts.RunTask() instead.
}

func (c *Client) start_service(data interface{}) (*ClientConn, error) {
	var cts *ClientConn
	var err error
	var cfg *ClientConfig
	var ok bool

	cfg, ok = data.(*ClientConfig)
	if !ok {
		err = fmt.Errorf("invalid configuration given")
		return nil, err
	}

	cts, err = c.AddNewClientConn(cfg)
	if err != nil {
		err = fmt.Errorf("unable to add server connection structure to %s - %s", cfg.ServerAddr, err.Error())
		return nil, err
	}

	c.wg.Add(1)
	go cts.RunTask(&c.wg)

	return cts, nil
}

func (c *Client) StartService(data interface{}) {
	var cts *ClientConn
	var err error

	cts, err = c.start_service(data)
	if err != nil {
		c.log.Write("", LOG_ERROR, "Failed to start service - %s", err.Error())
	} else {
		c.log.Write("", LOG_INFO, "Started service for %s [%d]", cts.cfg.ServerAddr, cts.cfg.Id)
	}
}

func (c *Client) StartExtService(svc Service, data interface{}) {
	c.ext_mtx.Lock()
	c.ext_svcs = append(c.ext_svcs, svc)
	c.ext_mtx.Unlock()
	c.wg.Add(1)
	go svc.RunTask(&c.wg)
}

func (c *Client) StopServices() {
	var ext_svc Service
	c.ReqStop()
	for _, ext_svc = range c.ext_svcs {
		ext_svc.StopServices()
	}
}

func (c *Client) WaitForTermination() {
	c.wg.Wait()
}

func (c *Client) WriteLog(id string, level LogLevel, fmtstr string, args ...interface{}) {
	c.log.Write(id, level, fmtstr, args...)
}
