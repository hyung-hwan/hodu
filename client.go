package hodu

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "log"
import "math/rand"
import "net"
import "net/http"
import "sync"
import "sync/atomic"
import "time"

import "google.golang.org/grpc"
import "google.golang.org/grpc/codes"
import "google.golang.org/grpc/credentials"
import "google.golang.org/grpc/credentials/insecure"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/status"

type PacketStreamClient grpc.BidiStreamingClient[Packet, Packet]

type ClientConnMap = map[uint32]*ClientConn
type ClientPeerConnMap = map[uint32]*ClientPeerConn
type ClientRouteMap = map[uint32]*ClientRoute
type ClientPeerCancelFuncMap = map[uint32]context.CancelFunc

// --------------------------------------------------------------------
type ClientConfig struct {
	ServerAddrs []string
	PeerAddrs []string
	ServerSeedTimeout int
	ServerAuthority string // http2 :authority header
}

type ClientConfigActive struct {
	Id uint32
	Index int
	ClientConfig
}

type Client struct {
	ctx         context.Context
	ctx_cancel  context.CancelFunc
	ctltlscfg  *tls.Config
	rpctlscfg  *tls.Config

	ext_mtx     sync.Mutex
	ext_svcs   []Service

	ctl_addr   []string
	ctl_prefix  string
	ctl_mux    *http.ServeMux
	ctl        []*http.Server // control server

	cts_mtx     sync.Mutex
	cts_map     ClientConnMap

	wg          sync.WaitGroup
	stop_req    atomic.Bool
	stop_chan   chan bool

	log         Logger

	stats struct {
		conns atomic.Int64
		routes atomic.Int64
		peers atomic.Int64
	}
}

// client connection to server
type ClientConn struct {
	cli        *Client
	cfg         ClientConfigActive
	id          uint32
	sid         string // id rendered in string

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
	server_peer_net string
	server_peer_proto ROUTE_PROTO

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
func NewClientRoute(cts *ClientConn, id uint32, client_peer_addr string, server_peer_net string, server_peer_proto ROUTE_PROTO) *ClientRoute {
	var r ClientRoute

	r.cts = cts
	r.id = id
	r.ptc_map = make(ClientPeerConnMap)
	r.ptc_cancel_map = make(ClientPeerCancelFuncMap)
	r.peer_addr = client_peer_addr // client-side peer
	r.server_peer_net = server_peer_net // permitted network for server-side peer
	r.server_peer_proto = server_peer_proto
	r.stop_req.Store(false)
	r.stop_chan = make(chan bool, 8)

	return &r
}

func (r *ClientRoute) AddNewClientPeerConn(c *net.TCPConn, pts_id uint32, pts_raddr string, pts_laddr string) (*ClientPeerConn, error) {
	var ptc *ClientPeerConn

	r.ptc_mtx.Lock()
	ptc = NewClientPeerConn(r, c, pts_id, pts_raddr, pts_laddr)
	r.ptc_map[ptc.conn_id] = ptc
	r.cts.cli.stats.peers.Add(1)
	r.ptc_mtx.Unlock()

	r.cts.cli.log.Write(r.cts.sid, LOG_INFO, "Added client-side peer(%d,%d,%s,%s)", r.id, ptc.conn_id, ptc.conn.RemoteAddr().String(), ptc.conn.LocalAddr().String())
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
		return fmt.Errorf("conflicting peer id - %d", ptc.conn_id)
	}
	delete(r.ptc_map, ptc.conn_id)
	r.cts.cli.stats.peers.Add(-1)
	r.ptc_mtx.Unlock()

	r.cts.cli.log.Write(r.cts.sid, LOG_INFO, "Removed client-side peer(%d,%d,%s,%s)", r.id, ptc.conn_id, ptc.conn.RemoteAddr().String(), ptc.conn.LocalAddr().String())
	ptc.ReqStop()
	return nil
}

/*func (r *ClientRoute) RemoveAllClientPeerConns() {
	var c *ClientPeerConn

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

	for _, c = range r.ptc_map {
		delete(r.ptc_map, c.conn_id)
		r.cts.cli.stats.peers.Add(-1)
		c.ReqStop()
	}
}*/

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

	err = r.cts.psc.Send(MakeRouteStartPacket(r.id, r.server_peer_proto, r.peer_addr, r.server_peer_net))
	if err != nil {
		r.cts.cli.log.Write(r.cts.sid, LOG_DEBUG,
			"Failed to send route_start for route(%d,%s,%v,%v) to %s",
			r.id, r.peer_addr, r.server_peer_proto, r.server_peer_net, r.cts.remote_addr)
		goto done
	} else {
		r.cts.cli.log.Write(r.cts.sid, LOG_DEBUG,
			"Sent route_start for route(%d,%s,%v,%v) to %s",
			r.id, r.peer_addr, r.server_peer_proto, r.server_peer_net, r.cts.remote_addr)
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

	err = r.cts.psc.Send(MakeRouteStopPacket(r.id, r.server_peer_proto, r.peer_addr, r.server_peer_net))
	if err != nil {
		r.cts.cli.log.Write(r.cts.sid, LOG_DEBUG,
			"Failed to route_stop for route(%d,%s,%v,%v) to %s - %s",
			r.id, r.peer_addr, r.server_peer_proto, r.server_peer_net, r.cts.remote_addr, err.Error())
	} else {
		r.cts.cli.log.Write(r.cts.sid, LOG_DEBUG,
			"Sent route_stop for route(%d,%s,%v,%v) to %s",
			r.id, r.peer_addr, r.server_peer_proto, r.server_peer_net, r.cts.remote_addr)
	}

	r.cts.RemoveClientRoute(r)
}

func (r *ClientRoute) ReqStop() {
	if r.stop_req.CompareAndSwap(false, true) {
		var ptc *ClientPeerConn
		for _, ptc = range r.ptc_map {
			ptc.ReqStop()
		}
		r.stop_chan <- true
	}
}

func (r *ClientRoute) ConnectToPeer(pts_id uint32, pts_raddr string, pts_laddr string, wg *sync.WaitGroup) {
	var err error
	var conn net.Conn
	var real_conn *net.TCPConn
	var real_conn_raddr string
	var real_conn_laddr string
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
		r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
			"Failed to connect to %s for route(%d,%d,%s,%s) - %s",
			r.peer_addr, r.id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	real_conn, ok = conn.(*net.TCPConn)
	if !ok {
		r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
			"Failed to get connection information to %s for route(%d,%d,%s,%s) - %s",
			r.peer_addr, r.id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	real_conn_raddr = real_conn.RemoteAddr().String()
	real_conn_laddr = real_conn.LocalAddr().String()
	ptc, err = r.AddNewClientPeerConn(real_conn, pts_id, pts_raddr, pts_laddr)
	if err != nil {
		r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
			"Failed to add client peer %s for route(%d,%d,%s,%s) - %s",
			r.peer_addr, r.id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	// ptc.conn is equal to pts_id as assigned in r.AddNewClientPeerConn()

	err = r.cts.psc.Send(MakePeerStartedPacket(r.id, ptc.conn_id, real_conn_raddr, real_conn_laddr))
	if err != nil {
		r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
			"Failed to send peer_start(%d,%d,%s,%s) for route(%d,%d,%s,%s) - %s",
			r.id, ptc.conn_id, real_conn_raddr, real_conn_laddr,
			r.id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	wg.Add(1)
	go ptc.RunTask(wg)
	return

peer_aborted:
	// real_conn_radd and real_conn_laddr may be empty depending on when the jump to here is made.
	err = r.cts.psc.Send(MakePeerAbortedPacket(r.id, pts_id, real_conn_raddr, real_conn_laddr))
	if err != nil {
		r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
			"Failed to send peer_aborted(%d,%d) for route(%d,%d,%s,%s) - %s",
			r.id, pts_id, r.id, pts_id, pts_raddr, pts_laddr, err.Error())
	}
	if conn != nil {
		conn.Close()
	}
}

func (r *ClientRoute) DisconnectFromPeer(ptc *ClientPeerConn) error {
	var p *ClientPeerConn
	var cancel context.CancelFunc
	var ok bool

	r.ptc_mtx.Lock()
	p, ok = r.ptc_map[ptc.conn_id]
	if ok && p == ptc {
		cancel, ok = r.ptc_cancel_map[ptc.conn_id]
		if ok {
			cancel()
		}
	}
	r.ptc_mtx.Unlock()

	ptc.ReqStop()
	return nil
}

func (r *ClientRoute) ReportEvent(pts_id uint32, event_type PACKET_KIND, event_data interface{}) error {
	var err error

	switch event_type {
		case PACKET_KIND_ROUTE_STARTED:
			var ok bool
			var rd *RouteDesc
			rd, ok = event_data.(*RouteDesc)
			if !ok {
				r.cts.cli.log.Write(r.cts.sid, LOG_ERROR, "Protocol error - invalid data in route_started event(%d)", r.id)
				r.ReqStop()
			} else {
				var addr *net.TCPAddr
				addr, err = net.ResolveTCPAddr("tcp", rd.TargetAddrStr)
				if err != nil {
					r.cts.cli.log.Write(r.cts.sid, LOG_ERROR, "Protocol error - invalid service address(%s) for server peer in route_started event(%d)", rd.TargetAddrStr, r.id)
					r.ReqStop()
				} else {
					r.server_peer_listen_addr = addr
					r.server_peer_net = rd.ServiceNetStr
				}
			}

		case PACKET_KIND_ROUTE_STOPPED:
			// NOTE:
			//  this event can be sent by the server in response to failed ROUTE_START or successful ROUTE_STOP.
			//  in case of the failed ROUTE_START, r.ReqStop() may trigger another ROUTE_STOP sent to the server.
			//  but the server must be able to handle this case as invalid route.
			var ok bool
			_, ok = event_data.(*RouteDesc)
			if !ok {
				r.cts.cli.log.Write(r.cts.sid, LOG_ERROR, "Protocol error - invalid data in route_started event(%d)", r.id)
				r.ReqStop()
			} else {
				r.ReqStop()
			}

		case PACKET_KIND_PEER_STARTED:
			var ok bool
			var pd *PeerDesc

			pd, ok = event_data.(*PeerDesc)
			if !ok {
				r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
					"Protocol error - invalid data in peer_started event(%d,%d)", r.id, pts_id)
				r.ReqStop()
			} else {
				r.ptc_wg.Add(1)
				go r.ConnectToPeer(pts_id, pd.RemoteAddrStr, pd.LocalAddrStr, &r.ptc_wg)
			}

		case PACKET_KIND_PEER_ABORTED:
			var ptc *ClientPeerConn

			ptc = r.FindClientPeerConnById(pts_id)
			if ptc != nil {
				var ok bool
				var pd *PeerDesc

				pd, ok = event_data.(*PeerDesc)
				if !ok {
					r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
						"Protocol error - invalid data in peer_aborted event(%d,%d)", r.id, pts_id)
					r.ReqStop()
				} else {
					err = r.DisconnectFromPeer(ptc)
					if err != nil {
						r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
							"Failed to disconnect from peer(%d,%d,%s,%s) - %s",
							r.id, pts_id, pd.RemoteAddrStr, pd.LocalAddrStr, err.Error())
						ptc.ReqStop()
					}
				}
			}

		case PACKET_KIND_PEER_STOPPED:
			var ptc *ClientPeerConn

			ptc = r.FindClientPeerConnById(pts_id)
			if ptc != nil {
				var ok bool
				var pd *PeerDesc

				pd, ok = event_data.(*PeerDesc)
				if !ok {
					r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
						"Protocol error - invalid data in peer_stopped event(%d,%d)",
						r.id, pts_id)
					ptc.ReqStop()
				} else {
					err = r.DisconnectFromPeer(ptc)
					if err != nil {
						r.cts.cli.log.Write(r.cts.sid, LOG_WARN,
							"Failed to disconnect from peer(%d,%d,%s,%s) - %s",
							r.id, pts_id, pd.RemoteAddrStr, pd.LocalAddrStr, err.Error())
						ptc.ReqStop()
					}
				}
			}

		case PACKET_KIND_PEER_EOF:
			var ptc *ClientPeerConn

			ptc = r.FindClientPeerConnById(pts_id)
			if ptc != nil {
				var ok bool

				_, ok = event_data.(*PeerDesc)
				if !ok {
					r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
						"Protocol error - invalid data in peer_eof event(%d,%d)",
						r.id, pts_id)
					ptc.ReqStop()
				} else {
					ptc.CloseWrite()
				}
			}

		case PACKET_KIND_PEER_DATA:
			var ptc *ClientPeerConn

			ptc = r.FindClientPeerConnById(pts_id)
			if ptc != nil {
				var ok bool
				var data []byte

				data, ok = event_data.([]byte)
				if !ok {
					r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
						"Protocol error - invalid data in peer_data event(%d,%d)",
						r.id, pts_id)
					ptc.ReqStop()
				} else {
					_, err = ptc.conn.Write(data)
					if err != nil {
						r.cts.cli.log.Write(r.cts.sid, LOG_ERROR,
							"Failed to write to peer(%d,%d,%s,%s) - %s",
							r.id, pts_id, ptc.conn.RemoteAddr().String(), ptc.conn.LocalAddr().String(), err.Error())
						ptc.ReqStop()
					}
				}
			}

		default:
			// ignore all others
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

func (cts *ClientConn) AddNewClientRoute(addr string, server_peer_net string, proto ROUTE_PROTO) (*ClientRoute, error) {
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
	r = NewClientRoute(cts, id, addr, server_peer_net, proto)
	cts.route_map[id] = r
	cts.cli.stats.routes.Add(1)
	cts.route_mtx.Unlock()

	cts.cli.log.Write(cts.sid, LOG_INFO, "Added route(%d,%s)", id, addr)

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

/*
func (cts *ClientConn) RemoveAllClientRoutes() {
	var r *ClientRoute

	cts.route_mtx.Lock()
	defer cts.route_mtx.Unlock()

	for _, r = range cts.route_map {
		delete(cts.route_map, r.id)
		cts.cli.stats.routes.Add(-1)
		r.ReqStop()
	}
}*/

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
		return fmt.Errorf("conflicting route id - %d", route.id)
	}
	delete(cts.route_map, route.id)
	cts.cli.stats.routes.Add(-1)
	cts.route_mtx.Unlock()

	cts.cli.log.Write(cts.sid, LOG_INFO, "Removed route(%d,%s)", route.id, route.peer_addr)

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
	cts.cli.stats.routes.Add(-1)
	cts.route_mtx.Unlock()

	cts.cli.log.Write(cts.sid, LOG_INFO, "Removed route(%d,%s)", r.id, r.peer_addr)

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
		_, err = cts.AddNewClientRoute(v, "", ROUTE_PROTO_TCP)
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

		cts.local_addr = ""
		cts.remote_addr = ""
	}
}

func (cts *ClientConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		cts.disconnect_from_server()
		cts.stop_chan <- true
	}
}

func timed_interceptor(tmout_sec int) grpc.UnaryClientInterceptor {
	// The client calls GetSeed() as the first call to the server.
	// To simulate a kind of connect timeout to the server and find out an unresponsive server,
	// Place a unary intercepter that places a new context with a timeout on the GetSeed() call.
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		var cancel context.CancelFunc
		if tmout_sec > 0 && method == Hodu_GetSeed_FullMethodName {
			ctx, cancel = context.WithTimeout(ctx, time.Duration(tmout_sec) * time.Second)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
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
	var opts []grpc.DialOption

	defer wg.Done() // arrange to call at the end of this function

start_over:
	cts.cfg.Index = (cts.cfg.Index + 1) % len(cts.cfg.ServerAddrs)
	cts.cli.log.Write(cts.sid, LOG_INFO, "Connecting to server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
	if cts.cli.rpctlscfg == nil {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if cts.cfg.ServerAuthority != "" { opts = append(opts, grpc.WithAuthority(cts.cfg.ServerAuthority)) }
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(cts.cli.rpctlscfg)))
		// set the http2 :authority header with tls server name defined.
		if cts.cfg.ServerAuthority != "" {
			opts = append(opts, grpc.WithAuthority(cts.cfg.ServerAuthority))
		} else if cts.cli.rpctlscfg.ServerName != "" {
			opts = append(opts, grpc.WithAuthority(cts.cli.rpctlscfg.ServerName))
		}
	}
	if cts.cfg.ServerSeedTimeout > 0 {
		opts = append(opts, grpc.WithUnaryInterceptor(timed_interceptor(cts.cfg.ServerSeedTimeout)))
	}

	cts.conn, err = grpc.NewClient(cts.cfg.ServerAddrs[cts.cfg.Index], opts...)
	if err != nil {
		cts.cli.log.Write(cts.sid, LOG_ERROR, "Failed to make client to server[%d] %s - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
		goto reconnect_to_server
	}
	cts.hdc = NewHoduClient(cts.conn)

// TODO: HANDLE connection timeout.. may have to run GetSeed or PacketStream in anther goroutnine
//	ctx, _/*cancel*/ := context.WithTimeout(context.Background(), time.Second)

	// seed exchange is for furture expansion of the protocol
	// there is nothing to do much about it for now.
	c_seed.Version = HODU_RPC_VERSION
	c_seed.Flags = 0
	s_seed, err = cts.hdc.GetSeed(cts.cli.ctx, &c_seed)
	if err != nil {
		cts.cli.log.Write(cts.sid, LOG_ERROR, "Failed to get seed from server[%d] %s - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
		goto reconnect_to_server
	}
	cts.s_seed = *s_seed
	cts.c_seed = c_seed

	cts.cli.log.Write(cts.sid, LOG_INFO, "Got seed from server[%d] %s - ver=%#x", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], cts.s_seed.Version)

	psc, err = cts.hdc.PacketStream(cts.cli.ctx)
	if err != nil {
		cts.cli.log.Write(cts.sid, LOG_ERROR, "Failed to get packet stream from server[%d] %s - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
		goto reconnect_to_server
	}

	p, ok = peer.FromContext(psc.Context())
	if ok {
		cts.remote_addr = p.Addr.String()
		cts.local_addr = p.LocalAddr.String()
	}

	cts.cli.log.Write(cts.sid, LOG_INFO, "Got packet stream from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])

	cts.psc = &GuardedPacketStreamClient{Hodu_PacketStreamClient: psc}

	if len(cts.cfg.PeerAddrs) > 0 {
		// the connection structure to a server is ready.
		// let's add routes to the client-side peers if given
		err = cts.AddClientRoutes(cts.cfg.PeerAddrs)
		if err != nil {
			cts.cli.log.Write(cts.sid, LOG_ERROR, "Failed to add routes to server[%s] %s for %v - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], cts.cfg.PeerAddrs, err.Error())
			goto done
		}
	}

	for {
		var pkt *Packet

		select {
			case <-cts.cli.ctx.Done():
				// need to log cts.cli.ctx.Err().Error()?
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
				cts.cli.log.Write(cts.sid, LOG_INFO, "Failed to receive packet from %s - %s", cts.remote_addr, err.Error())
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
					err = cts.ReportEvent(x.Route.RouteId, 0, pkt.Kind, x.Route)
					if err != nil {
						cts.cli.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle route_started event(%d,%s) from %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr, err.Error())
					} else {
						cts.cli.log.Write(cts.sid, LOG_DEBUG,
							"Handled route_started event(%d,%s) from %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr)
					}
				} else {
					cts.cli.log.Write(cts.sid, LOG_ERROR, "Invalid route_started event from %s", cts.remote_addr)
				}

			case PACKET_KIND_ROUTE_STOPPED:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					err = cts.ReportEvent(x.Route.RouteId, 0, pkt.Kind, x.Route)
					if err != nil {
						cts.cli.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle route_stopped event(%d,%s) from %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr, err.Error())
					} else {
						cts.cli.log.Write(cts.sid, LOG_DEBUG,
							"Handled route_stopped event(%d,%s) from %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr)
					}
				} else {
					cts.cli.log.Write(cts.sid, LOG_ERROR, "Invalid route_stopped event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_STARTED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STARTED, x.Peer)
					if err != nil {
						cts.cli.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_started event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.cli.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_started event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.cli.log.Write(cts.sid, LOG_ERROR, "Invalid peer_started event from %s", cts.remote_addr)
				}

			// PACKET_KIND_PEER_ABORTED is never sent by server to client.
			// the code here doesn't handle the event.

			case PACKET_KIND_PEER_STOPPED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STOPPED, x.Peer)
					if err != nil {
						cts.cli.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_stopped event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.cli.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_stopped event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.cli.log.Write(cts.sid, LOG_ERROR, "Invalid peer_stopped event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_EOF:
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_EOF, x.Peer)
					if err != nil {
						cts.cli.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_eof event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.cli.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_eof event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.cli.log.Write(cts.sid, LOG_ERROR, "Invalid peer_eof event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(x.Data.RouteId, x.Data.PeerId, PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
						cts.cli.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_data event from %s for peer(%d,%d) - %s",
							cts.remote_addr, x.Data.RouteId, x.Data.PeerId, err.Error())
					} else {
						cts.cli.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_data event from %s for peer(%d,%d)",
							cts.remote_addr, x.Data.RouteId, x.Data.PeerId)
					}
				} else {
					cts.cli.log.Write(cts.sid, LOG_ERROR, "Invalid peer_data event from %s", cts.remote_addr)
				}

			default:
				// do nothing. ignore the rest
		}
	}

done:
	cts.cli.log.Write(cts.sid, LOG_INFO, "Disconnected from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
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
			// need to log cts.cli.ctx.Err().Error()?
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

func (cts *ClientConn) ReportEvent(route_id uint32, pts_id uint32, event_type PACKET_KIND, event_data interface{}) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route_id)
	}
	cts.route_mtx.Unlock()

	return r.ReportEvent(pts_id, event_type, event_data)
}

// --------------------------------------------------------------------

type client_ctl_log_writer struct {
	cli *Client
}

func (hlw *client_ctl_log_writer) Write(p []byte) (n int, err error) {
	// the standard http.Server always requires *log.Logger
	// use this iowriter to create a logger to pass it to the http server.
	hlw.cli.log.Write("", LOG_INFO, string(p))
	return len(p), nil
}

func NewClient(ctx context.Context, ctl_addrs []string, logger Logger, ctl_prefix string, ctltlscfg *tls.Config, rpctlscfg *tls.Config) *Client {
	var c Client
	var i int
	var hs_log *log.Logger

	c.ctx, c.ctx_cancel = context.WithCancel(ctx)
	c.ctltlscfg = ctltlscfg
	c.rpctlscfg = rpctlscfg
	c.ext_svcs = make([]Service, 0, 1)
	c.cts_map = make(ClientConnMap)
	c.stop_req.Store(false)
	c.stop_chan = make(chan bool, 8)
	c.log = logger
	c.ctl_prefix = ctl_prefix

	c.ctl_mux = http.NewServeMux()
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns", &client_ctl_client_conns{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}", &client_ctl_client_conns_id{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes", &client_ctl_client_conns_id_routes{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes/{route_id}", &client_ctl_client_conns_id_routes_id{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes/{route_id}/peers", &client_ctl_client_conns_id_routes_id_peers{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/client-conns/{conn_id}/routes/{route_id}/peers/{peer_id}", &client_ctl_client_conns_id_routes_id_peers_id{c: &c})
	c.ctl_mux.Handle(c.ctl_prefix + "/stats", &client_ctl_stats{c: &c})

	c.ctl_addr = make([]string, len(ctl_addrs))
	c.ctl = make([]*http.Server, len(ctl_addrs))
	copy(c.ctl_addr, ctl_addrs)

	hs_log = log.New(&client_ctl_log_writer{cli: &c}, "", 0);

	for i = 0; i < len(ctl_addrs); i++ {
		c.ctl[i] = &http.Server{
			Addr: ctl_addrs[i],
			Handler: c.ctl_mux,
			TLSConfig: c.ctltlscfg,
			ErrorLog: hs_log,
			// TODO: more settings
		}
	}

	c.stats.conns.Store(0)
	c.stats.routes.Store(0)
	c.stats.peers.Store(0)

	return &c
}

func (c *Client) AddNewClientConn(cfg *ClientConfig) (*ClientConn, error) {
	var cts *ClientConn
	var ok bool
	var id uint32

	cts = NewClientConn(c, cfg)

	c.cts_mtx.Lock()

	id = rand.Uint32()
	for {
		_, ok = c.cts_map[id]
		if !ok { break }
		id++
	}
	cts.id = id
	cts.cfg.Id = id // store it again in the active configuration for easy access via control channel
	cts.sid = fmt.Sprintf("%d", id) // id in string used for logging

	c.cts_map[id] = cts
	c.stats.conns.Add(1)
	c.cts_mtx.Unlock()

	c.log.Write("", LOG_INFO, "Added client connection(%d) to %v", cts.id, cfg.ServerAddrs)
	return cts, nil
}

func (c *Client) ReqStopAllClientConns() {
	var cts *ClientConn

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	for _, cts = range c.cts_map {
		cts.ReqStop()
	}
}

/*
func (c *Client) RemoveAllClientConns() {
	var cts *ClientConn

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	for _, cts = range c.cts_map {
		delete(c.cts_map_by_addr, cts.cfg.ServerAddr)
		delete(c.cts_map, cts.id)
		c.stats.conns.Store(int64(len(c.cts_map)))
		cts.ReqStop()
	}
}
*/

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
		return fmt.Errorf("conflicting connection id - %d", cts.id)
	}

	delete(c.cts_map, cts.id)
	c.stats.conns.Store(int64(len(c.cts_map)))
	c.cts_mtx.Unlock()

	c.log.Write("", LOG_INFO, "Removed client connection(%d) to %v", cts.id, cts.cfg.ServerAddrs)

	cts.ReqStop()
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

	delete(c.cts_map, cts.id)
	c.stats.conns.Store(int64(len(c.cts_map)))
	c.cts_mtx.Unlock()

	c.log.Write("", LOG_INFO, "Removed client connection(%d) to %v", cts.id, cts.cfg.ServerAddrs)
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
	var r *ClientRoute
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
		var ctl *http.Server

		for _, ctl = range c.ctl {
			ctl.Shutdown(c.ctx) // to break c.ctl.ListenAndServe()
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
	var ctl *http.Server
	var idx int
	var l_wg sync.WaitGroup

	defer wg.Done()

	for idx, ctl = range c.ctl {
		l_wg.Add(1)
		go func(i int, cs *http.Server) {
			var l net.Listener

			c.log.Write("", LOG_INFO, "Control channel[%d] started on %s", i, c.ctl_addr[i])

			// defeat hard-coded "tcp" in ListenAndServe() and ListenAndServeTLS()
			// by creating the listener explicitly.
			//   err = cs.ListenAndServe()
			//   err = cs.ListenAndServeTLS("", "") // c.tlscfg must provide a certificate and a key
			l, err = net.Listen(tcp_addr_str_class(cs.Addr), cs.Addr)
			if err == nil {
				if c.ctltlscfg == nil {
					err = cs.Serve(l)
				} else {
					err = cs.ServeTLS(l, "", "") // c.ctltlscfg must provide a certificate and a key
				}
				l.Close()
			}
			if errors.Is(err, http.ErrServerClosed) {
				c.log.Write("", LOG_DEBUG, "Control channel[%d] ended", i)
			} else {
				c.log.Write("", LOG_ERROR, "Control channel[%d] error - %s", i, err.Error())
			}
			l_wg.Done()
		}(idx, ctl)
	}
	l_wg.Wait()
}

func (c *Client) StartCtlService() {
	c.wg.Add(1)
	go c.RunCtlTask(&c.wg)
}

func (c *Client) RunTask(wg *sync.WaitGroup) {
	// just a place holder to pacify the Service interface
	// StartService() calls cts.RunTask() instead. it is not called.
	// so no call to wg.Done()
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
		err = fmt.Errorf("unable to add server connection structure to %v - %s", cfg.ServerAddrs, err.Error())
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
		c.log.Write("", LOG_INFO, "Started service for %v [%d]", cts.cfg.ServerAddrs, cts.cfg.Id)
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
