package hodu

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "log"
import "net"
import "net/http"
import "strconv"
import "sync"
import "sync/atomic"
import "time"
import "unsafe"

import "google.golang.org/grpc"
import "google.golang.org/grpc/codes"
import "google.golang.org/grpc/credentials"
import "google.golang.org/grpc/credentials/insecure"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/status"
import "github.com/prometheus/client_golang/prometheus"
import "github.com/prometheus/client_golang/prometheus/promhttp"

type PacketStreamClient grpc.BidiStreamingClient[Packet, Packet]

type ClientConnMap = map[ConnId]*ClientConn
type ClientRouteMap = map[RouteId]*ClientRoute
type ClientPeerConnMap = map[PeerId]*ClientPeerConn
type ClientPeerCancelFuncMap = map[PeerId]context.CancelFunc

// --------------------------------------------------------------------
type ClientRouteConfig struct {
	Id          RouteId // requested id to be assigned. 0 for automatic assignment
	PeerAddr    string
	PeerName    string
	Option      RouteOption
	ServiceAddr string // server-peer-service-addr
	ServiceNet  string // server-peer-service-net
	Lifetime    time.Duration
	Static      bool
}

type ClientConnConfig struct {
	ServerAddrs []string
	Routes      []ClientRouteConfig
	ServerSeedTmout time.Duration
	ServerAuthority string // http2 :authority header
}

type ClientConnConfigActive struct {
	Index int
	ClientConnConfig
}

type ClientConfig struct {
	CtlAddrs []string
	CtlTls *tls.Config
	CtlPrefix string
	CtlAuth *HttpAuthConfig
	CtlCors bool

	RpcTls *tls.Config
	RpcConnMax int
	PeerConnMax int
	PeerConnTmout time.Duration
}

type ClientConnNoticeHandler interface {
	Handle(cts *ClientConn, text string)
}

type Client struct {
	Named

	Ctx        context.Context
	CtxCancel  context.CancelFunc

	ext_mtx    sync.Mutex
	ext_svcs   []Service

	rpc_tls   *tls.Config
	ctl_tls   *tls.Config
	ctl_addr   []string
	ctl_prefix string
	ctl_cors   bool
	ctl_auth   *HttpAuthConfig
	ctl_mux    *http.ServeMux
	ctl        []*http.Server // control server

	ptc_tmout   time.Duration // timeout seconds to connect to peer
	ptc_limit   int // global maximum number of peers
	cts_limit   int
	cts_mtx     sync.Mutex
	cts_next_id ConnId
	cts_map     ClientConnMap

	wg          sync.WaitGroup
	stop_req    atomic.Bool
	stop_chan   chan bool

	log             Logger
	conn_notice     ClientConnNoticeHandler
	route_persister ClientRoutePersister

	promreg *prometheus.Registry
	stats struct {
		conns atomic.Int64
		routes atomic.Int64
		peers atomic.Int64
	}
}

type ClientConnState int

const  (
	CLIENT_CONN_CONNECTING ClientConnState = iota
	CLIENT_CONN_CONNECTED
	CLIENT_CONN_DISCONNECTING
	CLIENT_CONN_DISCONNECTED
)

// client connection to server
type ClientConn struct {
	C            *Client
	cfg           ClientConnConfigActive
	Id            ConnId
	Sid           string // id rendered in string
	State         ClientConnState

	local_addr    string
	remote_addr   string
	conn         *grpc.ClientConn // grpc connection to the server
	hdc           HoduClient
	psc          *GuardedPacketStreamClient // guarded grpc stream

	s_seed        Seed
	c_seed        Seed

	route_mtx     sync.Mutex
	route_next_id RouteId
	route_map     ClientRouteMap
	route_wg      sync.WaitGroup

	stop_req      atomic.Bool
	stop_chan     chan bool
}

type ClientRoute struct {
	cts *ClientConn
	Id RouteId
	Static bool

	PeerAddr string
	PeerName string
	PeerOption RouteOption

	server_peer_listen_addr *net.TCPAddr // actual service-side service address
	ServerPeerAddr string // desired server-side service address
	ServerPeerNet string
	ServerPeerOption RouteOption

	ptc_mtx        sync.Mutex
	ptc_map        ClientPeerConnMap
	ptc_cancel_map ClientPeerCancelFuncMap
	ptc_wg         sync.WaitGroup

	Lifetime time.Duration
	LifetimeStart time.Time
	lifetime_timer *time.Timer
	lifetime_mtx sync.Mutex

	stop_req atomic.Bool
	stop_chan chan bool
}

type ClientPeerConn struct {
	route *ClientRoute
	conn_id PeerId
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

type ClientRoutePersister interface {
	LoadAll(cts *ClientConn)
	Save(cts *ClientConn, r *ClientRoute)
	Delete(cts *ClientConn, r *ClientRoute)
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
func NewClientRoute(cts *ClientConn, id RouteId, static bool, client_peer_addr string, client_peer_name string, server_peer_svc_addr string, server_peer_svc_net string, server_peer_option RouteOption, lifetime time.Duration) *ClientRoute {
	var r ClientRoute

	r.cts = cts
	r.Id = id
	r.Static = static
	r.ptc_map = make(ClientPeerConnMap)
	r.ptc_cancel_map = make(ClientPeerCancelFuncMap)
	r.PeerAddr = client_peer_addr // client-side peer
	r.PeerName = client_peer_name
	// if the client_peer_addr is a domain name, it can't tell between tcp4 and tcp6
	r.PeerOption = StringToRouteOption(TcpAddrStrClass(client_peer_addr))

	r.ServerPeerAddr = server_peer_svc_addr
	r.ServerPeerNet = server_peer_svc_net // permitted network for server-side peer
	r.ServerPeerOption = server_peer_option
	r.LifetimeStart = time.Now()
	r.Lifetime = lifetime
	r.stop_req.Store(false)
	r.stop_chan = make(chan bool, 8)

	return &r
}

func (r *ClientRoute) AddNewClientPeerConn(c *net.TCPConn, pts_id PeerId, pts_raddr string, pts_laddr string) (*ClientPeerConn, error) {
	var ptc *ClientPeerConn

	r.ptc_mtx.Lock()
	ptc = NewClientPeerConn(r, c, pts_id, pts_raddr, pts_laddr)
	r.ptc_map[ptc.conn_id] = ptc
	r.cts.C.stats.peers.Add(1)
	r.ptc_mtx.Unlock()

	r.cts.C.log.Write(r.cts.Sid, LOG_INFO, "Added client-side peer(%d,%d,%s,%s)", r.Id, ptc.conn_id, ptc.conn.RemoteAddr().String(), ptc.conn.LocalAddr().String())
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
	r.cts.C.stats.peers.Add(-1)
	r.ptc_mtx.Unlock()

	r.cts.C.log.Write(r.cts.Sid, LOG_INFO, "Removed client-side peer(%d,%d,%s,%s)", r.Id, ptc.conn_id, ptc.conn.RemoteAddr().String(), ptc.conn.LocalAddr().String())
	ptc.ReqStop()
	return nil
}

func (r *ClientRoute) ReqStopAllClientPeerConns() {
	var c *ClientPeerConn

	r.ptc_mtx.Lock()
	for _, c = range r.ptc_map { c.ReqStop() }
	r.ptc_mtx.Unlock()
}

func (r *ClientRoute) FindClientPeerConnById(peer_id PeerId) *ClientPeerConn {
	var c *ClientPeerConn
	var ok bool

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

	c, ok = r.ptc_map[peer_id]
	if !ok { return nil }

	return c
}

func (r *ClientRoute) ExtendLifetime(lifetime time.Duration) error {
	r.lifetime_mtx.Lock()
	defer r.lifetime_mtx.Unlock()
	if r.lifetime_timer == nil {
		// let's not support timer extend if route was not
		// first started with lifetime enabled
		return fmt.Errorf("prohibited operation")
	} else {
		var expiry time.Time
		r.lifetime_timer.Stop()
		r.Lifetime = r.Lifetime + lifetime
		expiry = r.LifetimeStart.Add(r.Lifetime)
		r.lifetime_timer.Reset(expiry.Sub(time.Now()))
		if r.cts.C.route_persister != nil { r.cts.C.route_persister.Save(r.cts, r) }
		return nil
	}
}

func (r *ClientRoute) ResetLifetime(lifetime time.Duration) error {
	r.lifetime_mtx.Lock()
	defer r.lifetime_mtx.Unlock()
	if r.lifetime_timer == nil {
		// let's not support timer reset if route was not
		// first started with lifetime enabled
		return fmt.Errorf("prohibited operation")
	} else {
		r.lifetime_timer.Stop()
		r.Lifetime = lifetime
		r.LifetimeStart = time.Now()
		r.lifetime_timer.Reset(lifetime)
		if r.cts.C.route_persister != nil { r.cts.C.route_persister.Save(r.cts, r) }
		return nil
	}
}

func (r *ClientRoute) RunTask(wg *sync.WaitGroup) {
	var err error

	// this task on the route object do actual data manipulation
	// most useful works are triggered by ReportEvent() and done by ConnectToPeer()
	// it merely implements some timeout if set.
	defer wg.Done()

	err = r.cts.psc.Send(MakeRouteStartPacket(r.Id, r.ServerPeerOption, r.PeerAddr, r.PeerName, r.ServerPeerAddr, r.ServerPeerNet))
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Failed to send route_start for route(%d,%s,%v,%v) to %s",
			r.Id, r.PeerAddr, r.ServerPeerOption, r.ServerPeerNet, r.cts.remote_addr)
		goto done
	} else {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Sent route_start for route(%d,%s,%v,%v) to %s",
			r.Id, r.PeerAddr, r.ServerPeerOption, r.ServerPeerNet, r.cts.remote_addr)
	}

	r.lifetime_mtx.Lock()
	if r.Lifetime > 0 {
		r.LifetimeStart = time.Now()
		r.lifetime_timer = time.NewTimer(r.Lifetime)
	}
	r.lifetime_mtx.Unlock()

main_loop:
	for {
		if r.lifetime_timer != nil {
			select {
				case <-r.stop_chan:
					break main_loop

				case <-r.lifetime_timer.C:
					r.cts.C.log.Write(r.cts.Sid, LOG_INFO, "route(%d,%s,%v,%v) reached end of lifetime(%v)",
						r.Id, r.PeerAddr, r.ServerPeerOption, r.ServerPeerNet, r.Lifetime)
					break main_loop
			}
		} else {
			select {
				case <-r.stop_chan:
					break main_loop
			}
		}
	}

	r.lifetime_mtx.Lock()
	if r.lifetime_timer != nil {
		r.lifetime_timer.Stop()
		r.lifetime_timer = nil
	}
	r.lifetime_mtx.Unlock()

done:
	r.ReqStop()
	r.ptc_wg.Wait() // wait for all peer tasks are finished

	err = r.cts.psc.Send(MakeRouteStopPacket(r.Id, r.ServerPeerOption, r.PeerAddr, r.PeerName, r.ServerPeerAddr, r.ServerPeerNet))
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Failed to route_stop for route(%d,%s,%v,%v) to %s - %s",
			r.Id, r.PeerAddr, r.ServerPeerOption, r.ServerPeerNet, r.cts.remote_addr, err.Error())
	} else {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Sent route_stop for route(%d,%s,%v,%v) to %s",
			r.Id, r.PeerAddr, r.ServerPeerOption, r.ServerPeerNet, r.cts.remote_addr)
	}

	r.cts.RemoveClientRoute(r)
}

func (r *ClientRoute) ReqStop() {
	if r.stop_req.CompareAndSwap(false, true) {
		var ptc *ClientPeerConn

		r.ptc_mtx.Lock()
		for _, ptc = range r.ptc_map { ptc.ReqStop() }
		r.ptc_mtx.Unlock()

		r.stop_chan <- true
	}
}

func (r *ClientRoute) ConnectToPeer(pts_id PeerId, route_option RouteOption, pts_raddr string, pts_laddr string, wg *sync.WaitGroup) {
	var err error
	var conn net.Conn
	var real_conn *net.TCPConn
	var real_conn_raddr string
	var real_conn_laddr string
	var ptc *ClientPeerConn
	var d net.Dialer
	var waitctx context.Context
	var cancel_wait context.CancelFunc
	var tmout time.Duration
	var ok bool

// TODO: handle TTY
//	if route_option & RouteOption(ROUTE_OPTION_TTY) it must create a pseudo-tty insteaad of connecting to tcp address
//

	defer wg.Done()

	tmout = time.Duration(r.cts.C.ptc_tmout)
	if tmout <= 0 { tmout = 5 * time.Second} // TODO: make this configurable...
	waitctx, cancel_wait = context.WithTimeout(r.cts.C.Ctx, tmout)
	r.ptc_mtx.Lock()
	r.ptc_cancel_map[pts_id] = cancel_wait
	r.ptc_mtx.Unlock()

	d.LocalAddr = nil // TOOD: use this if local address is specified
	conn, err = d.DialContext(waitctx, TcpAddrStrClass(r.PeerAddr), r.PeerAddr)

	r.ptc_mtx.Lock()
	cancel_wait()
	delete(r.ptc_cancel_map, pts_id)
	r.ptc_mtx.Unlock()

	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
			"Failed to connect to %s for route(%d,%d,%s,%s) - %s",
			r.PeerAddr, r.Id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	real_conn, ok = conn.(*net.TCPConn)
	if !ok {
		r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
			"Failed to get connection information to %s for route(%d,%d,%s,%s) - %s",
			r.PeerAddr, r.Id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	real_conn_raddr = real_conn.RemoteAddr().String()
	real_conn_laddr = real_conn.LocalAddr().String()
	ptc, err = r.AddNewClientPeerConn(real_conn, pts_id, pts_raddr, pts_laddr)
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
			"Failed to add client peer %s for route(%d,%d,%s,%s) - %s",
			r.PeerAddr, r.Id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	// ptc.conn is equal to pts_id as assigned in r.AddNewClientPeerConn()

	err = r.cts.psc.Send(MakePeerStartedPacket(r.Id, ptc.conn_id, real_conn_raddr, real_conn_laddr))
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
			"Failed to send peer_start(%d,%d,%s,%s) for route(%d,%d,%s,%s) - %s",
			r.Id, ptc.conn_id, real_conn_raddr, real_conn_laddr,
			r.Id, pts_id, pts_raddr, pts_laddr, err.Error())
		goto peer_aborted
	}

	wg.Add(1)
	go ptc.RunTask(wg)
	return

peer_aborted:
	// real_conn_radd and real_conn_laddr may be empty depending on when the jump to here is made.
	err = r.cts.psc.Send(MakePeerAbortedPacket(r.Id, pts_id, real_conn_raddr, real_conn_laddr))
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
			"Failed to send peer_aborted(%d,%d) for route(%d,%d,%s,%s) - %s",
			r.Id, pts_id, r.Id, pts_id, pts_raddr, pts_laddr, err.Error())
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
		if ok { cancel() }
	}
	r.ptc_mtx.Unlock()

	ptc.ReqStop()
	return nil
}

func (r *ClientRoute) ReportEvent(pts_id PeerId, event_type PACKET_KIND, event_data interface{}) error {
	var err error

	switch event_type {
		case PACKET_KIND_ROUTE_STARTED:
			var ok bool
			var rd *RouteDesc
			rd, ok = event_data.(*RouteDesc)
			if !ok {
				r.cts.C.log.Write(r.cts.Sid, LOG_ERROR, "Protocol error - invalid data in route_started event(%d)", r.Id)
				r.ReqStop()
			} else {
				var addr *net.TCPAddr
				addr, err = net.ResolveTCPAddr(TcpAddrStrClass(rd.TargetAddrStr), rd.TargetAddrStr)
				if err != nil {
					r.cts.C.log.Write(r.cts.Sid, LOG_ERROR, "Protocol error - invalid service address(%s) for server peer in route_started event(%d)", rd.TargetAddrStr, r.Id)
					r.ReqStop()
				} else {
					r.server_peer_listen_addr = addr
					r.ServerPeerNet = rd.ServiceNetStr
				}
			}

		case PACKET_KIND_ROUTE_STOPPED:
			// NOTE:
			//  this event can be sent by the server in response to failed ROUTE_START or successful ROUTE_STOP.
			//  in case of the failed ROUTE_START, r.ReqStop() may trigger another ROUTE_STOP sent to the server.
			//  but the server must be able to handle this case as invalid route.
			var ok bool
			_, ok = event_data.(*RouteDesc)
			if !ok { r.cts.C.log.Write(r.cts.Sid, LOG_ERROR, "Protocol error - invalid data in route_started event(%d)", r.Id) }
			r.ReqStop()

		case PACKET_KIND_PEER_STARTED:
			var ok bool
			var pd *PeerDesc

			pd, ok = event_data.(*PeerDesc)
			if !ok {
				r.cts.C.log.Write(r.cts.Sid, LOG_WARN,
					"Protocol error - invalid data in peer_started event(%d,%d)", r.Id, pts_id)
				// ignore it. don't want to delete the whole route
			} else {
				if r.cts.C.ptc_limit > 0 && int(r.cts.C.stats.peers.Load()) >= r.cts.C.ptc_limit {
					r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
						"Rejecting to connect to peer(%s)for route(%d,%d) - allowed max %d",
						r.PeerAddr, r.Id, pts_id, r.cts.C.ptc_limit)

					err = r.cts.psc.Send(MakePeerAbortedPacket(r.Id, pts_id, "", ""))
					if err != nil {
						r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
							"Failed to send peer_aborted(%d,%d) for route(%d,%d,%s,%s) - %s",
							r.Id, pts_id, r.Id, pts_id, "", "", err.Error())
					}
				} else {
					r.ptc_wg.Add(1)
					go r.ConnectToPeer(pts_id, r.PeerOption, pd.RemoteAddrStr, pd.LocalAddrStr, &r.ptc_wg)
				}
			}

		case PACKET_KIND_PEER_ABORTED:
			var ptc *ClientPeerConn

			ptc = r.FindClientPeerConnById(pts_id)
			if ptc != nil {
				var ok bool
				var pd *PeerDesc

				pd, ok = event_data.(*PeerDesc)
				if !ok {
					r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
						"Protocol error - invalid data in peer_aborted event(%d,%d)", r.Id, pts_id)
					ptc.ReqStop()
				} else {
					err = r.DisconnectFromPeer(ptc)
					if err != nil {
						r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
							"Failed to disconnect from peer(%d,%d,%s,%s) - %s",
							r.Id, pts_id, pd.RemoteAddrStr, pd.LocalAddrStr, err.Error())
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
					r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
						"Protocol error - invalid data in peer_stopped event(%d,%d)",
						r.Id, pts_id)
					ptc.ReqStop()
				} else {
					err = r.DisconnectFromPeer(ptc)
					if err != nil {
						r.cts.C.log.Write(r.cts.Sid, LOG_WARN,
							"Failed to disconnect from peer(%d,%d,%s,%s) - %s",
							r.Id, pts_id, pd.RemoteAddrStr, pd.LocalAddrStr, err.Error())
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
					r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
						"Protocol error - invalid data in peer_eof event(%d,%d)",
						r.Id, pts_id)
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
					r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
						"Protocol error - invalid data in peer_data event(%d,%d)",
						r.Id, pts_id)
					ptc.ReqStop()
				} else {
					_, err = ptc.conn.Write(data)
					if err != nil {
						r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
							"Failed to write to peer(%d,%d,%s,%s) - %s",
							r.Id, pts_id, ptc.conn.RemoteAddr().String(), ptc.conn.LocalAddr().String(), err.Error())
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
func NewClientConn(c *Client, cfg *ClientConnConfig) *ClientConn {
	var cts ClientConn
	var i int

	cts.C = c
	cts.route_map = make(ClientRouteMap)
	cts.route_next_id = 1
	cts.cfg.ClientConnConfig = *cfg
	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 8)

	for i, _ = range cts.cfg.Routes {
		// override it to static regardless of the value passed in
		cts.cfg.Routes[i].Static = true
	}

	// the actual connection to the server is established in the main task function
	// The cts.conn, cts.hdc, cts.psc fields are left unassigned here.

	return &cts
}

func (cts *ClientConn) AddNewClientRoute(rc *ClientRouteConfig) (*ClientRoute, error) {
	var r *ClientRoute
	var assigned_id RouteId

	cts.route_mtx.Lock()
	if rc.Id <= 0 {
		// perform automatic assignemnt
		var start_id RouteId

		//start_id = RouteId(rand.Uint64())
		start_id = cts.route_next_id
		for {
			var ok bool
			_, ok = cts.route_map[cts.route_next_id]
			if !ok {
				assigned_id = cts.route_next_id
				cts.route_next_id++
				if cts.route_next_id == 0 { cts.route_next_id++ }
				break
			}
			cts.route_next_id++
			if cts.route_next_id == 0 { cts.route_next_id++ } // skip 0 as it is a marker for auto-assignment
			if cts.route_next_id == start_id {
				cts.route_mtx.Unlock()
				return nil, fmt.Errorf("unable to assign id")
			}
		}
	} else {
		var ok bool
		_, ok = cts.route_map[rc.Id]
		if ok {
			cts.route_mtx.Unlock()
			return nil, fmt.Errorf("id(%d) unavailable", rc.Id)
		}
		assigned_id = rc.Id
	}

	r = NewClientRoute(cts, assigned_id, rc.Static, rc.PeerAddr, rc.PeerName, rc.ServiceAddr, rc.ServiceNet, rc.Option, rc.Lifetime)
	cts.route_map[r.Id] = r
	cts.C.stats.routes.Add(1)
	if cts.C.route_persister != nil { cts.C.route_persister.Save(cts, r) }
	cts.route_mtx.Unlock()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Added route(%d,%d) %s", cts.Id, r.Id, r.PeerAddr)

	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ClientConn) ReqStopAllClientRoutes() {
	var r *ClientRoute
	cts.route_mtx.Lock()
	for _, r = range cts.route_map { r.ReqStop() }
	cts.route_mtx.Unlock()
}

/*
func (cts *ClientConn) RemoveAllClientRoutes() {
	var r *ClientRoute

	cts.route_mtx.Lock()
	defer cts.route_mtx.Unlock()

	for _, r = range cts.route_map {
		delete(cts.route_map, r.Id)
		cts.C.stats.routes.Add(-1)
		r.ReqStop()
	}
}*/

func (cts *ClientConn) RemoveClientRoute(route *ClientRoute) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route.Id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route.Id)
	}
	if r != route {
		cts.route_mtx.Unlock()
		return fmt.Errorf("conflicting route id - %d", route.Id)
	}
	delete(cts.route_map, route.Id)
	cts.C.stats.routes.Add(-1)
	if cts.C.route_persister != nil {
		cts.C.route_persister.Delete(cts, r)
	}
	cts.route_mtx.Unlock()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Removed route(%d,%s)", route.Id, route.PeerAddr)

	r.ReqStop()
	return nil
}

func (cts *ClientConn) RemoveClientRouteById(route_id RouteId) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route_id)
	}
	delete(cts.route_map, route_id)
	cts.C.stats.routes.Add(-1)
	if cts.C.route_persister != nil { cts.C.route_persister.Delete(cts, r) }
	cts.route_mtx.Unlock()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Removed route(%d,%s)", r.Id, r.PeerAddr)

	r.ReqStop()
	return nil
}

func (cts *ClientConn) RemoveClientRouteByServerPeerSvcPortId(port_id PortId) error {
	var r *ClientRoute

	// this is slow as there is no indexing by the service side port id
	// the use of this function is not really recommended. the best is to
	// use the actual route id for finding.
	cts.route_mtx.Lock()
	for _, r = range cts.route_map {
		if r.server_peer_listen_addr.Port == int(port_id) {
			delete(cts.route_map, r.Id)
			cts.C.stats.routes.Add(-1)
			if cts.C.route_persister != nil { cts.C.route_persister.Delete(cts, r) }
			cts.route_mtx.Unlock()
			cts.C.log.Write(cts.Sid, LOG_INFO, "Removed route(%d,%s)", r.Id, r.PeerAddr)
			r.ReqStop()
			return nil
		}
	}
	cts.route_mtx.Unlock()

	return fmt.Errorf("non-existent server peer service port id - %d", port_id)
}

func (cts *ClientConn) FindClientRouteById(route_id RouteId) *ClientRoute {
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

func (cts *ClientConn) FindClientRouteByServerPeerSvcPortId(port_id PortId) *ClientRoute {
	var r *ClientRoute

	// this is slow as there is no indexing by the service side port id
	// the use of this function is not really recommended. the best is to
	// use the actual route id for finding.
	cts.route_mtx.Lock()
	for _, r = range cts.route_map {
		if r.server_peer_listen_addr.Port == int(port_id) {
			cts.route_mtx.Unlock()
			return r; // return the first match
		}
	}
	cts.route_mtx.Unlock()

	return nil
}

func (cts *ClientConn) add_client_routes(routes []ClientRouteConfig) error {
	var v ClientRouteConfig
	var err error

	for _, v = range routes {
		_, err = cts.AddNewClientRoute(&v)
		if err != nil {
			return fmt.Errorf("unable to add client route for %v - %s", v, err.Error())
		}
	}

	return nil
}

func (cts *ClientConn) disconnect_from_server() {
	if cts.conn != nil {
		var r *ClientRoute

		cts.route_mtx.Lock()
		for _, r = range cts.route_map { r.ReqStop() }
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

func timed_interceptor(tmout time.Duration) grpc.UnaryClientInterceptor {
	// The client calls GetSeed() as the first call to the server.
	// To simulate a kind of connect timeout to the server and find out an unresponsive server,
	// Place a unary intercepter that places a new context with a timeout on the GetSeed() call.
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		var cancel context.CancelFunc
		if tmout > 0 && method == Hodu_GetSeed_FullMethodName {
			ctx, cancel = context.WithTimeout(ctx, tmout)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (cts *ClientConn) RunTask(wg *sync.WaitGroup) {
	var psc PacketStreamClient
	var slpctx context.Context
	var cancel_sleep context.CancelFunc
	var c_seed Seed
	var s_seed *Seed
	var p *peer.Peer
	var ok bool
	var err error
	var opts []grpc.DialOption

	defer wg.Done() // arrange to call at the end of this function

start_over:
	cts.State = CLIENT_CONN_CONNECTING
	cts.cfg.Index = (cts.cfg.Index + 1) % len(cts.cfg.ServerAddrs)
	cts.C.log.Write(cts.Sid, LOG_INFO, "Connecting to server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
	if cts.C.rpc_tls == nil {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if cts.cfg.ServerAuthority != "" { opts = append(opts, grpc.WithAuthority(cts.cfg.ServerAuthority)) }
	} else {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(cts.C.rpc_tls)))
		// set the http2 :authority header with tls server name defined.
		if cts.cfg.ServerAuthority != "" {
			opts = append(opts, grpc.WithAuthority(cts.cfg.ServerAuthority))
		} else if cts.C.rpc_tls.ServerName != "" {
			opts = append(opts, grpc.WithAuthority(cts.C.rpc_tls.ServerName))
		}
	}
	if cts.cfg.ServerSeedTmout > 0 {
		opts = append(opts, grpc.WithUnaryInterceptor(timed_interceptor(cts.cfg.ServerSeedTmout)))
	}

	cts.conn, err = grpc.NewClient(cts.cfg.ServerAddrs[cts.cfg.Index], opts...)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to make client to server[%d] %s - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
		goto reconnect_to_server
	}
	cts.hdc = NewHoduClient(cts.conn)

	// seed exchange is for furture expansion of the protocol
	// there is nothing to do much about it for now.
	c_seed.Version = HODU_RPC_VERSION
	c_seed.Flags = 0
	s_seed, err = cts.hdc.GetSeed(cts.C.Ctx, &c_seed)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to get seed from server[%d] %s - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
		goto reconnect_to_server
	}
	cts.s_seed = *s_seed
	cts.c_seed = c_seed
	cts.route_next_id = 1 // reset this whenever a new connection is made. the number of routes must be zero.

	cts.C.log.Write(cts.Sid, LOG_INFO, "Got seed from server[%d] %s - ver=%#x", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], cts.s_seed.Version)

	psc, err = cts.hdc.PacketStream(cts.C.Ctx)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to get packet stream from server[%d] %s - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
		goto reconnect_to_server
	}

	p, ok = peer.FromContext(psc.Context())
	if ok {
		cts.remote_addr = p.Addr.String()
		cts.local_addr = p.LocalAddr.String()
	}

	cts.C.log.Write(cts.Sid, LOG_INFO, "Got packet stream from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])

	cts.State = CLIENT_CONN_CONNECTED

	cts.psc = &GuardedPacketStreamClient{Hodu_PacketStreamClient: psc}

	if len(cts.cfg.Routes) > 0 {
		// the connection structure to a server is ready.
		// let's add statically configured routes to the client-side peers
		err = cts.add_client_routes(cts.cfg.Routes)
		if err != nil {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to add routes to server[%d] %s for %v - %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], cts.cfg.Routes, err.Error())
			goto done
		}
	}

	if cts.C.route_persister != nil {
		// restore the client routes added and saved via the control channel
		cts.C.route_persister.LoadAll(cts)
	}

	for {
		var pkt *Packet

		select {
			case <-cts.C.Ctx.Done():
				// need to log cts.C.Ctx.Err().Error()?
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
				cts.C.log.Write(cts.Sid, LOG_INFO, "Failed to receive packet from %s - %s", cts.remote_addr, err.Error())
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
					err = cts.ReportEvent(RouteId(x.Route.RouteId), 0, pkt.Kind, x.Route)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle route_started event(%d,%s) from %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled route_started event(%d,%s) from %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid route_started event from %s", cts.remote_addr)
				}

			case PACKET_KIND_ROUTE_STOPPED:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					err = cts.ReportEvent(RouteId(x.Route.RouteId), 0, pkt.Kind, x.Route)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle route_stopped event(%d,%s) from %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled route_stopped event(%d,%s) from %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid route_stopped event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_STARTED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), PACKET_KIND_PEER_STARTED, x.Peer)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_started event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_started event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_started event from %s", cts.remote_addr)
				}

			// PACKET_KIND_PEER_ABORTED is never sent by server to client.
			// the code here doesn't handle the event.

			case PACKET_KIND_PEER_STOPPED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), PACKET_KIND_PEER_STOPPED, x.Peer)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_stopped event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_stopped event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_stopped event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_EOF:
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), PACKET_KIND_PEER_EOF, x.Peer)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_eof event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_eof event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_eof event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(RouteId(x.Data.RouteId), PeerId(x.Data.PeerId), PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_data event from %s for peer(%d,%d) - %s",
							cts.remote_addr, x.Data.RouteId, x.Data.PeerId, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_data event from %s for peer(%d,%d)",
							cts.remote_addr, x.Data.RouteId, x.Data.PeerId)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_data event from %s", cts.remote_addr)
				}

			case PACKET_KIND_CONN_NOTICE:
				// the connection from the client to a peer has been established
				var x *Packet_Notice
				var ok bool
				x, ok = pkt.U.(*Packet_Notice)
				if ok {
					if cts.C.conn_notice != nil {
						cts.C.conn_notice.Handle(cts, x.Notice.Text)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid conn_data event from %s", cts.remote_addr)
				}

			default:
				// do nothing. ignore the rest
		}
	}

done:
	cts.C.log.Write(cts.Sid, LOG_INFO, "Disconnected from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
	cts.State = CLIENT_CONN_DISCONNECTED

req_stop_and_wait_for_termination:
	//cts.RemoveClientRoutes() // this isn't needed as each task removes itself from cts upon its termination
	cts.ReqStop()

wait_for_termination:
	cts.route_wg.Wait() // wait until all route tasks are finished
	cts.C.RemoveClientConn(cts)
	return

reconnect_to_server:
	cts.State = CLIENT_CONN_DISCONNECTING

	if cts.conn != nil {
		cts.C.log.Write(cts.Sid, LOG_INFO, "Disconnecting from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
	}
	cts.disconnect_from_server()
	cts.State = CLIENT_CONN_DISCONNECTED

	// wait for 2 seconds
	slpctx, cancel_sleep = context.WithTimeout(cts.C.Ctx, 2 * time.Second)
	select {
		case <-cts.C.Ctx.Done():
			// need to log cts.C.Ctx.Err().Error()?
			cancel_sleep()
			goto req_stop_and_wait_for_termination
		case <-cts.stop_chan:
			// this signal indicates that ReqStop() has been called
			// so jump to the waiting label
			cancel_sleep()
			goto wait_for_termination
		case <-slpctx.Done():
			select {
				case <- cts.C.Ctx.Done():
					// non-blocking check if the parent context of the sleep context is
					// terminated too. if so, this is normal termination case.
					// this check seem redundant but the go-runtime doesn't seem to guarantee
					// the order of event selection whtn cts.C.Ctx.Done() and slpctx.Done()
					// are both completed.
					goto req_stop_and_wait_for_termination
				default:
					// do nothing. go on
			}
	}
	cancel_sleep()
	goto start_over // and reconnect
}

func (cts *ClientConn) ReportEvent(route_id RouteId, pts_id PeerId, event_type PACKET_KIND, event_data interface{}) error {
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

type ClientHttpHandler interface {
	Id() string
	Cors(req *http.Request) bool
	Authenticate(req *http.Request) (int, string)
	ServeHTTP (w http.ResponseWriter, req *http.Request) (int, error)
}

func (c *Client) wrap_http_handler(handler ClientHttpHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var status_code int
		var err error
		var start_time time.Time
		var time_taken time.Duration
		var realm string

		// this deferred function is to overcome the recovering implemenation
		// from panic done in go's http server. in that implemenation, panic
		// is isolated to a single gorountine. however, i want this program
		// to exit immediately once a panic condition is caught. (e.g. nil
		// pointer dererence)
		defer func() {
			var err interface{} = recover()
			if err != nil { dump_call_frame_and_exit(c.log, req, err) }
		}()

		start_time = time.Now()

		if handler.Cors(req) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Allow-Methods", "*")
		}
		if req.Method == http.MethodOptions {
			status_code = WriteEmptyRespHeader(w, http.StatusOK)
		} else {
			status_code, realm = handler.Authenticate(req)
			if status_code == http.StatusUnauthorized {
				if realm != "" {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic Realm=\"%s\"", realm))
				}
				WriteEmptyRespHeader(w, status_code)
			} else if status_code == http.StatusOK {
				status_code, err = handler.ServeHTTP(w, req)
			} else {
				WriteEmptyRespHeader(w, status_code)
			}
		}

		// TODO: statistics by status_code and end point types.
		time_taken = time.Now().Sub(start_time)

		if status_code > 0 {
			if err == nil {
				c.log.Write(handler.Id(), LOG_INFO, "[%s] %s %s %d %.9f", req.RemoteAddr, req.Method, req.URL.String(), status_code, time_taken.Seconds())
			} else {
				c.log.Write(handler.Id(), LOG_INFO, "[%s] %s %s %d %.9f - %s", req.RemoteAddr, req.Method, req.URL.String(), status_code, time_taken.Seconds(), err.Error())
			}
		}
	})
}

func NewClient(ctx context.Context, name string, logger Logger, cfg *ClientConfig) *Client {
	var c Client
	var i int
	var hs_log *log.Logger

	c.name = name
	c.Ctx, c.CtxCancel = context.WithCancel(ctx)
	c.ext_svcs = make([]Service, 0, 1)
	c.ptc_tmout = cfg.PeerConnTmout
	c.ptc_limit = cfg.PeerConnMax
	c.cts_limit = cfg.RpcConnMax
	c.cts_next_id = 1
	c.cts_map = make(ClientConnMap)
	c.stop_req.Store(false)
	c.stop_chan = make(chan bool, 8)
	c.log = logger

	c.rpc_tls = cfg.RpcTls
	c.ctl_auth = cfg.CtlAuth
	c.ctl_tls = cfg.CtlTls
	c.ctl_prefix = cfg.CtlPrefix
	c.ctl_cors = cfg.CtlCors
	c.ctl_mux = http.NewServeMux()
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns",
		c.wrap_http_handler(&client_ctl_client_conns{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}",
		c.wrap_http_handler(&client_ctl_client_conns_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes",
		c.wrap_http_handler(&client_ctl_client_conns_id_routes{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes/{route_id}",
		c.wrap_http_handler(&client_ctl_client_conns_id_routes_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes-spsp/{port_id}",
		c.wrap_http_handler(&client_ctl_client_conns_id_routes_spsp{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes/{route_id}/peers",
		c.wrap_http_handler(&client_ctl_client_conns_id_routes_id_peers{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes/{route_id}/peers/{peer_id}",
		c.wrap_http_handler(&client_ctl_client_conns_id_routes_id_peers_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/notices",
		c.wrap_http_handler(&client_ctl_notices{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/notices/{conn_id}",
		c.wrap_http_handler(&client_ctl_notices_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/stats",
		c.wrap_http_handler(&client_ctl_stats{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/token",
		c.wrap_http_handler(&client_ctl_token{client_ctl{c: &c, id: HS_ID_CTL}}))

// TODO: make this optional. add this endpoint only if it's enabled...
	c.promreg = prometheus.NewRegistry()
	c.promreg.MustRegister(prometheus.NewGoCollector())
	c.promreg.MustRegister(NewClientCollector(&c))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/metrics",
		promhttp.HandlerFor(c.promreg, promhttp.HandlerOpts{ EnableOpenMetrics: true }))


	c.ctl_addr = make([]string, len(cfg.CtlAddrs))
	c.ctl = make([]*http.Server, len(cfg.CtlAddrs))
	copy(c.ctl_addr, cfg.CtlAddrs)

	hs_log = log.New(&client_ctl_log_writer{cli: &c}, "", 0)

	for i = 0; i < len(cfg.CtlAddrs); i++ {
		c.ctl[i] = &http.Server{
			Addr: cfg.CtlAddrs[i],
			Handler: c.ctl_mux,
			TLSConfig: c.ctl_tls,
			ErrorLog: hs_log,
			// TODO: more settings
		}
	}

	c.stats.conns.Store(0)
	c.stats.routes.Store(0)
	c.stats.peers.Store(0)

	return &c
}

func (c *Client) AddNewClientConn(cfg *ClientConnConfig) (*ClientConn, error) {
	var cts *ClientConn
	var ok bool
	var start_id ConnId
	var assigned_id ConnId

	if len(cfg.ServerAddrs) <= 0 {
		return nil, fmt.Errorf("no server rpc address specified")
	}

	cts = NewClientConn(c, cfg)

	c.cts_mtx.Lock()

	if c.cts_limit > 0 && len(c.cts_map) >= c.cts_limit {
		c.cts_mtx.Unlock()
		return nil, fmt.Errorf("too many connections - %d", c.cts_limit)
	}

	//start_id = rand.Uint64()
	//start_id = ConnId(monotonic_time() / 1000)
	start_id = c.cts_next_id
	for {
		_, ok = c.cts_map[c.cts_next_id]
		if !ok {
			assigned_id = c.cts_next_id
			c.cts_next_id++
			if c.cts_next_id == 0 { c.cts_next_id++ }
			break
		}
		c.cts_next_id++
		if c.cts_next_id == 0 { c.cts_next_id++ }
		if c.cts_next_id == start_id {
			c.cts_mtx.Lock()
			return nil, fmt.Errorf("unable to assign id")
		}
	}

	cts.Id = assigned_id
	cts.Sid = fmt.Sprintf("%d", cts.Id) // id in string used for logging

	c.cts_map[cts.Id] = cts
	c.stats.conns.Add(1)
	c.cts_mtx.Unlock()

	c.log.Write("", LOG_INFO, "Added client connection(%d) to %v", cts.Id, cfg.ServerAddrs)
	return cts, nil
}

func (c *Client) ReqStopAllClientConns() {
	var cts *ClientConn
	c.cts_mtx.Lock()
	for _, cts = range c.cts_map { cts.ReqStop() }
	c.cts_mtx.Unlock()
}

/*
func (c *Client) RemoveAllClientConns() {
	var cts *ClientConn

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	for _, cts = range c.cts_map {
		delete(c.cts_map_by_addr, cts.cfg.ServerAddr)
		delete(c.cts_map, cts.Id)
		c.stats.conns.Store(int64(len(c.cts_map)))
		cts.ReqStop()
	}
}
*/

func (c *Client) RemoveClientConn(cts *ClientConn) error {
	var conn *ClientConn
	var ok bool

	c.cts_mtx.Lock()

	conn, ok = c.cts_map[cts.Id]
	if !ok {
		c.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.Id)
	}
	if conn != cts {
		c.cts_mtx.Unlock()
		return fmt.Errorf("conflicting connection id - %d", cts.Id)
	}

	delete(c.cts_map, cts.Id)
	c.stats.conns.Store(int64(len(c.cts_map)))
	c.cts_mtx.Unlock()

	c.log.Write("", LOG_INFO, "Removed client connection(%d) to %v", cts.Id, cts.cfg.ServerAddrs)

	cts.ReqStop()
	return nil
}

func (c *Client) RemoveClientConnById(conn_id ConnId) error {
	var cts *ClientConn
	var ok bool

	c.cts_mtx.Lock()

	cts, ok = c.cts_map[conn_id]
	if !ok {
		c.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", conn_id)
	}

	// NOTE: removal by id doesn't perform identity check

	delete(c.cts_map, cts.Id)
	c.stats.conns.Store(int64(len(c.cts_map)))
	c.cts_mtx.Unlock()

	c.log.Write("", LOG_INFO, "Removed client connection(%d) to %v", cts.Id, cts.cfg.ServerAddrs)
	cts.ReqStop()
	return nil
}

func (c *Client) FindClientConnById(id ConnId) *ClientConn {
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

func (c *Client) FindClientRouteById(conn_id ConnId, route_id RouteId) *ClientRoute {
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

func (c *Client) FindClientConnByIdStr(conn_id string) (*ClientConn, error) {
	var conn_nid uint64
	var cts *ClientConn
	var err error

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error()); }

	cts = c.FindClientConnById(ConnId(conn_nid))
	if cts == nil { return nil, fmt.Errorf("non-existent connection id %d", conn_nid) }

	return cts, nil
}

func (c *Client) FindClientRouteByServerPeerSvcPortId(conn_id ConnId, port_id PortId) *ClientRoute {
	var cts *ClientConn
	var ok bool

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	cts, ok = c.cts_map[conn_id]
	if !ok {
		return nil
	}

	return cts.FindClientRouteByServerPeerSvcPortId(port_id)
}

func (c *Client) FindClientPeerConnById(conn_id ConnId, route_id RouteId, peer_id PeerId) *ClientPeerConn {
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

		c.CtxCancel()
		for _, ctl = range c.ctl {
			ctl.Shutdown(c.Ctx) // to break c.ctl.ListenAndServe()
		}

		c.cts_mtx.Lock()
		for _, cts = range c.cts_map { cts.ReqStop() }
		c.cts_mtx.Unlock()

		c.stop_chan <- true
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

			//cs.shuttingDown(), as the name indicates, is not expoosed by the net/http
			//so I have to use my own indicator to check if it's been shutdown..
			//
			if c.stop_req.Load() == false {
				// this guard has a flaw in that the stop request can be made
				// between the check above and net.Listen() below.
				l, err = net.Listen(TcpAddrStrClass(cs.Addr), cs.Addr)
				if err == nil {
					if c.stop_req.Load() == false {
						// check it again to make the guard slightly more stable
						// although it's still possible that the stop request is made
						// after Listen()
						if c.ctl_tls == nil {
							err = cs.Serve(l)
						} else {
							err = cs.ServeTLS(l, "", "") // c.ctl_tls must provide a certificate and a key
						}
					} else {
						err = fmt.Errorf("stop requested")
					}
					l.Close()
				}
			} else {
				err = fmt.Errorf("stop requested")
			}
			if errors.Is(err, http.ErrServerClosed) {
				c.log.Write("", LOG_INFO, "Control channel[%d] ended", i)
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

func (c *Client) start_service(cfg *ClientConnConfig) (*ClientConn, error) {
	var cts *ClientConn
	var err error

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
	var cfg *ClientConnConfig
	var ok bool

	cfg, ok = data.(*ClientConnConfig)
	if !ok {
		c.log.Write("", LOG_ERROR, "Failed to start service - invalid configuration - %v", data)
	} else {
		var cts *ClientConn
		var err error

		if len(cfg.ServerAddrs) > 0 {
			cts, err = c.start_service(cfg)
			if err != nil {
				c.log.Write("", LOG_ERROR, "Failed to start service - %s", err.Error())
			} else {
				c.log.Write("", LOG_INFO, "Started service for %v [%d]", cts.cfg.ServerAddrs, cts.Id)
			}
		}
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

func (c *Client) FixServices() {
	c.log.Rotate()
}

func (c *Client) WaitForTermination() {
	c.wg.Wait()
}

func (c *Client) WriteLog(id string, level LogLevel, fmtstr string, args ...interface{}) {
	c.log.Write(id, level, fmtstr, args...)
}

func (c *Client) SetConnNoticeHandler(handler ClientConnNoticeHandler) {
	c.conn_notice = handler
}

func (c *Client) SetRoutePersister(persister ClientRoutePersister) {
	c.route_persister = persister
}

func (c *Client) AddCtlMetricsCollector(col prometheus.Collector) error {
	return c.promreg.Register(col)
}

func (c *Client) RemoveCtlMetricsCollector(col prometheus.Collector) bool {
	return c.promreg.Unregister(col)
}
