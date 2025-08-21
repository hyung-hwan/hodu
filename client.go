package hodu

import "bufio"
import "bytes"
import "container/list"
import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "io"
import "log"
import "net"
import "net/http"
import "os"
import "os/exec"
import "slices"
import "strconv"
import "strings"
import "sync"
import "sync/atomic"
import "time"
import "unsafe"

import "golang.org/x/net/websocket"
import "golang.org/x/sys/unix"

import "google.golang.org/grpc"
import "google.golang.org/grpc/codes"
import "google.golang.org/grpc/credentials"
import "google.golang.org/grpc/credentials/insecure"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/status"

import pts "github.com/creack/pty"
import "github.com/prometheus/client_golang/prometheus"
import "github.com/prometheus/client_golang/prometheus/promhttp"

type PacketStreamClient grpc.BidiStreamingClient[Packet, Packet]

type ClientConnMap map[ConnId]*ClientConn
type ClientRouteMap map[RouteId]*ClientRoute
type ClientPeerConnMap map[PeerId]*ClientPeerConn
type ClientPeerCancelFuncMap map[PeerId]context.CancelFunc
type ClientRptyMap map[uint64]*ClientRpty
type ClientRpxMap map[uint64]*ClientRpx

// --------------------------------------------------------------------
type ClientRouteConfig struct {
	Id          RouteId // requested id to be assigned. 0 for automatic assignment
	PeerAddr      string
	PeerName      string
	ServiceAddr   string // server-peer-svc-addr
	ServiceNet    string // server-peer-svc-net
	ServiceOption RouteOption
	Lifetime      time.Duration
	Static        bool
}

type ClientConnConfig struct {
	ServerAddrs []string
	Routes      []ClientRouteConfig
	ServerSeedTmout time.Duration
	ServerAuthority string // http2 :authority header
	ClientToken string
	CloseOnConnErrorEvent bool
}

type ClientConnConfigActive struct {
	Index int
	ClientConnConfig
}

type ClientConnNoticeHandler interface {
	Handle(cts* ClientConn, text string)
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

	Token string // to send to the server for identification

	// default target for rpx
	RpxTargetAddr string
	RpxTargetTls *tls.Config
}

type ClientEventKind int
const (
	CLIENT_EVENT_CONN_STARTED = iota
	CLIENT_EVENT_CONN_UPDATED
	CLIENT_EVENT_CONN_STOPPED
	CLIENT_EVENT_ROUTE_STARTED
	CLIENT_EVENT_ROUTE_UPDATED
	CLIENT_EVENT_ROUTE_STOPPED
	CLIENT_EVENT_PEER_STARTED
	CLIENT_EVENT_PEER_UPDATED
	CLIENT_EVENT_PEER_STOPPED
)

type ClientEvent struct {
	Kind ClientEventKind `json:"type"`
	Data interface{} `json:"data"`
}

type ClientEventBulletin = Bulletin[*ClientEvent]
type ClientEventSubscription = BulletinSubscription[*ClientEvent]

type Client struct {
	Named

	Ctx        context.Context
	CtxCancel  context.CancelFunc

	wg          sync.WaitGroup
	stop_req    atomic.Bool
	stop_chan   chan bool
	token       string

	ext_mtx    sync.Mutex
	ext_svcs   []Service

	rpc_tls   *tls.Config
	rpx_target_addr string // may get appended with :port
	rpx_target_addr_org string // original as given by the user
	rpx_target_url string
	rpx_target_tls  *tls.Config

	ctl_tls   *tls.Config
	ctl_addr   []string
	ctl_prefix string
	ctl_cors   bool
	ctl_auth   *HttpAuthConfig
	ctl_mux    *http.ServeMux
	ctl        []*http.Server // control server

	ptc_tmout   time.Duration // timeout seconds to connect to peer
	cts_limit   int
	cts_mtx     sync.Mutex
	cts_next_id ConnId
	cts_map     ClientConnMap

	ptc_limit   int // global maximum number of peers
	ptc_mtx     sync.Mutex
	ptc_list    *list.List

	route_mtx   sync.Mutex
	route_list  *list.List

	log             Logger
	conn_notice_handlers []ClientConnNoticeHandler
	route_persister ClientRoutePersister

	bulletin        *ClientEventBulletin

	promreg *prometheus.Registry
	stats struct {
		conns atomic.Int64
		routes atomic.Int64
		peers atomic.Int64
		pty_sessions atomic.Int64
		rpty_sessions atomic.Int64
		rpx_sessions atomic.Int64
	}

	pty_user string
	pty_shell string
	xterm_html string
}

type ClientConnState = int32
const  (
	CLIENT_CONN_CONNECTING ClientConnState = iota
	CLIENT_CONN_CONNECTED
	CLIENT_CONN_DISCONNECTING
	CLIENT_CONN_DISCONNECTED
)

type ClientRpty struct {
	id uint64
	cmd *exec.Cmd
	tty *os.File
}

type ClientRpx struct {
	id uint64
	pr *io.PipeReader
	pw *io.PipeWriter
	ctx context.Context
	cancel context.CancelFunc
	ws_conn net.Conn
}

// client connection to server
type ClientConn struct {
	C            *Client
	cfg           ClientConnConfigActive
	Id            ConnId
	Sid           string // id rendered in string
	Created       time.Time
	State         atomic.Int32 // ClientConnState
	Token         Atom[string]

	local_addr    Atom[string]
	remote_addr   Atom[string]
	local_addr_p  string
	remote_addr_p string

	conn         *grpc.ClientConn // grpc connection to the server
	hdc           HoduClient
	psc          *GuardedPacketStreamClient // guarded grpc stream

	s_seed        Seed
	c_seed        Seed

	route_mtx     sync.Mutex
	route_next_id RouteId
	route_map     ClientRouteMap
	route_wg      sync.WaitGroup

	ptc_mtx       sync.Mutex
	ptc_list      *list.List

	rpty_mtx      sync.Mutex
	rpty_map      ClientRptyMap

	rpx_mtx      sync.Mutex
	rpx_map      ClientRpxMap

	stop_req      atomic.Bool
	stop_chan     chan bool

	discon_mtx    sync.Mutex
}

type ClientRoute struct {
	cts *ClientConn
	Id RouteId
	Created time.Time
	Static bool

	node_in_client *list.Element

	PeerAddr string
	PeerName string
	PeerOption RouteOption // internally used in connecting

	ReqServerPeerSvcAddr string // requested server-side service address
	ReqServerPeerSvcNet string // requested server-side service address
	ServerPeerListenAddr *net.TCPAddr // actual service-side service address
	ServerPeerSvcAddr Atom[string]  // actual server-side service address
	ServerPeerSvcNet Atom[string] // actual server-side network
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
	Created time.Time

	node_in_client *list.Element
	node_in_conn *list.Element

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

func (rpty *ClientRpty) ReqStop() {
	rpty.tty.Close()
	rpty.cmd.Process.Kill()
}

func (rpx *ClientRpx) ReqStop() {
	rpx.pr.Close()
	rpx.pw.Close()
	rpx.cancel()
	if rpx.ws_conn != nil {
		rpx.ws_conn.SetDeadline(time.Now()) // to make Read return immediately
	}
}

// --------------------------------------------------------------------
func NewClientRoute(cts *ClientConn, id RouteId, static bool, client_peer_addr string, client_peer_name string, server_peer_svc_addr string, server_peer_svc_net string, server_peer_option RouteOption, lifetime time.Duration) *ClientRoute {
	var r ClientRoute

	r.cts = cts
	r.Id = id
	r.Created = time.Now()
	r.Static = static
	r.ptc_map = make(ClientPeerConnMap)
	r.ptc_cancel_map = make(ClientPeerCancelFuncMap)
	r.PeerAddr = client_peer_addr // client-side peer
	r.PeerName = client_peer_name
	// if the client_peer_addr is a domain name, it can't tell between tcp4 and tcp6
	r.PeerOption = StringToRouteOption(TcpAddrStrClass(client_peer_addr))

	r.ReqServerPeerSvcAddr = server_peer_svc_addr
	r.ReqServerPeerSvcNet = server_peer_svc_net // permitted network for server-side peer
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

	r.cts.C.ptc_mtx.Lock()
	ptc.node_in_client = r.cts.C.ptc_list.PushBack(ptc)
	r.cts.C.ptc_mtx.Unlock()

	r.cts.ptc_mtx.Lock()
	ptc.node_in_conn = r.cts.ptc_list.PushBack(ptc)
	r.cts.ptc_mtx.Unlock()

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
	if r.lifetime_timer == nil {
		// let's not support timer extend if route was not
		// first started with lifetime enabled
		r.lifetime_mtx.Unlock()
		return fmt.Errorf("prohibited operation")
	} else {
		var expiry time.Time
		r.lifetime_timer.Stop()
		r.Lifetime = r.Lifetime + lifetime
		expiry = r.LifetimeStart.Add(r.Lifetime)
		r.lifetime_timer.Reset(time.Until(expiry)) // expiry.Sub(time.Now())
		if r.cts.C.route_persister != nil { r.cts.C.route_persister.Save(r.cts, r) }
		r.lifetime_mtx.Unlock()

		r.cts.C.FireRouteEvent(CLIENT_EVENT_ROUTE_UPDATED, r)
		return nil
	}
}

func (r *ClientRoute) ResetLifetime(lifetime time.Duration) error {
	r.lifetime_mtx.Lock()
	if r.lifetime_timer == nil {
		// let's not support timer reset if route was not
		// first started with lifetime enabled
		r.lifetime_mtx.Unlock()
		return fmt.Errorf("prohibited operation")
	} else {
		r.lifetime_timer.Stop()
		r.Lifetime = lifetime
		r.LifetimeStart = time.Now()
		r.lifetime_timer.Reset(lifetime)
		if r.cts.C.route_persister != nil { r.cts.C.route_persister.Save(r.cts, r) }
		r.lifetime_mtx.Unlock()

		r.cts.C.FireRouteEvent(CLIENT_EVENT_ROUTE_UPDATED, r)
		return nil
	}
}

func (r *ClientRoute) GetLifetimeInfo() (time.Time, time.Duration) {
	r.lifetime_mtx.Lock()
	defer r.lifetime_mtx.Unlock()
	return r.LifetimeStart, r.Lifetime
}

func (r *ClientRoute) RunTask(wg *sync.WaitGroup) {
	var err error

	// this task on the route object do actual data manipulation
	// most useful works are triggered by ReportPacket() and done by ConnectToPeer()
	// it merely implements some timeout if set.
	defer wg.Done()

	r.cts.C.FireRouteEvent(CLIENT_EVENT_ROUTE_STARTED, r)

	err = r.cts.psc.Send(MakeRouteStartPacket(r.Id, r.ServerPeerOption, r.PeerAddr, r.PeerName, r.ReqServerPeerSvcAddr, r.ReqServerPeerSvcNet))
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Failed to send route_start for route(%d,%s,%v,%s,%s) to %s - %s",
			r.Id, r.PeerAddr, r.ServerPeerOption, r.ReqServerPeerSvcAddr, r.ReqServerPeerSvcNet, r.cts.remote_addr_p, err.Error())
		goto done
	} else {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Sent route_start for route(%d,%s,%v,%s,%s) to %s",
			r.Id, r.PeerAddr, r.ServerPeerOption, r.ReqServerPeerSvcAddr, r.ReqServerPeerSvcNet, r.cts.remote_addr_p)
	}

	r.ptc_wg.Add(1) // increment counter here

	go func() { // and run the waiting loop in a goroutine to give a positive counter to r.ptc_wg.Wait()
		r.lifetime_mtx.Lock()
		if r.Lifetime > 0 {
			r.LifetimeStart = time.Now()
			r.lifetime_timer = time.NewTimer(r.Lifetime)
		}
		r.lifetime_mtx.Unlock()

	waiting_loop:
		for {
			if r.lifetime_timer != nil {
				select {
					case <-r.stop_chan:
						break waiting_loop

					case <-r.lifetime_timer.C:
						r.cts.C.log.Write(r.cts.Sid, LOG_INFO, "route(%d,%s) reached end of lifetime(%v)",
							r.Id, r.PeerAddr, r.Lifetime)
						break waiting_loop
				}
			} else {
				<-r.stop_chan
				break waiting_loop
			}
		}

		r.lifetime_mtx.Lock()
		if r.lifetime_timer != nil {
			r.lifetime_timer.Stop()
			r.lifetime_timer = nil
		}
		r.lifetime_mtx.Unlock()

		r.ReqStop() // just in case
		r.ptc_wg.Done()
	}()

done:
	r.ptc_wg.Wait() // wait for all peer tasks are finished

	err = r.cts.psc.Send(MakeRouteStopPacket(r.Id, r.ServerPeerOption, r.PeerAddr, r.PeerName, r.ReqServerPeerSvcAddr, r.ReqServerPeerSvcNet))
	if err != nil {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Failed to route_stop for route(%d,%s) to %s - %s",
			r.Id, r.PeerAddr, r.cts.remote_addr_p, err.Error())
	} else {
		r.cts.C.log.Write(r.cts.Sid, LOG_DEBUG,
			"Sent route_stop for route(%d,%s) to %s",
			r.Id, r.PeerAddr, r.cts.remote_addr_p)
	}

	r.cts.RemoveClientRoute(r)

	r.cts.C.route_mtx.Lock()
	r.cts.C.route_list.Remove(r.node_in_client)
	r.node_in_client = nil
	r.cts.C.route_mtx.Unlock()

	r.cts.C.FireRouteEvent(CLIENT_EVENT_ROUTE_STOPPED, r)
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

	defer wg.Done()

	tmout = time.Duration(r.cts.C.ptc_tmout)
	if tmout <= 0 { tmout = 5 * time.Second } // TODO: make this configurable...
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
			"Failed to get connection information to %s for route(%d,%d,%s,%s)",
			r.PeerAddr, r.Id, pts_id, pts_raddr, pts_laddr)
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

func (r *ClientRoute) ReportPacket(pts_id PeerId, packet_type PACKET_KIND, event_data interface{}) error {
	var err error

	switch packet_type {
		case PACKET_KIND_ROUTE_STARTED:
			var ok bool
			var rd *RouteDesc
			rd, ok = event_data.(*RouteDesc)
			if !ok {
				r.cts.C.log.Write(r.cts.Sid, LOG_ERROR,
					"Protocol error - invalid data in route_started event(%d)", r.Id)
				r.ReqStop()
			} else {
				var addr *net.TCPAddr
				addr, err = net.ResolveTCPAddr(TcpAddrStrClass(rd.TargetAddrStr), rd.TargetAddrStr)
				if err != nil {
					r.cts.C.log.Write(r.cts.Sid,LOG_ERROR,
						"Protocol error - invalid service address(%s) for server peer in route_started event(%d)", rd.TargetAddrStr, r.Id)
					r.ReqStop()
				} else {
					// received the server-side addresses
					r.ServerPeerListenAddr = addr
					r.ServerPeerSvcAddr.Set(rd.TargetAddrStr)
					r.ServerPeerSvcNet.Set(rd.ServiceNetStr)

					r.cts.C.FireRouteEvent(CLIENT_EVENT_ROUTE_UPDATED, r)

					r.cts.C.log.Write(r.cts.Sid, LOG_INFO,
						"Ingested route_started(%d,%s,%s) for route(%d,%s,%v,%s,%s)",
							rd.RouteId, rd.TargetAddrStr, rd.ServiceNetStr,
							r.Id, r.PeerAddr, r.ServerPeerOption, r.ReqServerPeerSvcAddr, r.ReqServerPeerSvcNet)
				}
			}

		case PACKET_KIND_ROUTE_STOPPED:
			// NOTE:
			//  this event can be sent by the server in response to failed ROUTE_START or successful ROUTE_STOP.
			//  in case of the failed ROUTE_START, r.ReqStop() may trigger another ROUTE_STOP sent to the server.
			//  but the server must be able to handle this case as invalid route.
			var ok bool
			var rd *RouteDesc

			rd, ok = event_data.(*RouteDesc)
			if !ok {
				r.cts.C.log.Write(r.cts.Sid, LOG_ERROR, "Protocol error - invalid data in route_started event(%d)", r.Id)
			} else {
				r.cts.C.log.Write(r.cts.Sid, LOG_INFO,
					"Ingested route_stopped(%d,%s,%s) for route(%d,%s,%v,%s,%s)",
						rd.RouteId, rd.TargetAddrStr, rd.ServiceNetStr,
						r.Id, r.PeerAddr, r.ServerPeerOption, r.ReqServerPeerSvcAddr, r.ReqServerPeerSvcNet)
			}
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
	cts.Created = time.Now()
	cts.route_map = make(ClientRouteMap)
	cts.route_next_id = 1
	cts.cfg.ClientConnConfig = *cfg
	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 8)
	cts.ptc_list = list.New()
	cts.rpty_map = make(ClientRptyMap)
	cts.rpx_map = make(ClientRpxMap)

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

	r = NewClientRoute(cts, assigned_id, rc.Static, rc.PeerAddr, rc.PeerName, rc.ServiceAddr, rc.ServiceNet, rc.ServiceOption, rc.Lifetime)
	cts.route_map[r.Id] = r
	cts.C.stats.routes.Add(1)
	if cts.C.route_persister != nil { cts.C.route_persister.Save(cts, r) }
	cts.route_mtx.Unlock()

	cts.C.route_mtx.Lock()
	r.node_in_client = cts.C.route_list.PushBack(r)
	cts.C.route_mtx.Unlock()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Added route(%d,%s)", r.Id, r.PeerAddr)

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
	if cts.C.route_persister != nil { cts.C.route_persister.Delete(cts, r) }
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
		if r.ServerPeerListenAddr.Port == int(port_id) {
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
		if r.ServerPeerListenAddr.Port == int(port_id) {
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

func (cts *ClientConn) disconnect_from_server(logmsg bool) {
	if cts.conn != nil {
		var r *ClientRoute
		var rpty *ClientRpty
		var rpx *ClientRpx

		cts.discon_mtx.Lock()

		if (logmsg) {
			cts.C.log.Write(cts.Sid, LOG_INFO, "Disconnecting from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
		}

		cts.route_mtx.Lock()
		for _, r = range cts.route_map { r.ReqStop() }
		cts.route_mtx.Unlock()

		// arrange to clean up all rpty objects
		cts.rpty_mtx.Lock()
		for _, rpty = range cts.rpty_map {
			rpty.ReqStop()
			// the loop in ReadRptyLoop() is supposed to be broken.
			// let's not inform the server of this connection.
			// the server should clean up itself upon connection error
		}
		cts.rpty_mtx.Unlock()

		cts.rpx_mtx.Lock()
		for _, rpx = range cts.rpx_map {
			rpx.ReqStop()
		}
		cts.rpx_mtx.Unlock()

		// don't care about double closes when this function is called from both RunTask() and ReqStop()
		cts.conn.Close()

		// don't reset cts.conn to nil here
		// if this function is called from RunTask()
		// for reconnection, it will be set to a new value
		// immediately after the start_over lable in it.
		// if it's called from ReqStop(), we don't really
		// need to care about it.

		cts.local_addr.Set("")
		cts.remote_addr.Set("")
		// don't reset cts.local_addr_p and cts.remote_addr_p

		cts.discon_mtx.Unlock()
	}
}

func (cts *ClientConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		cts.disconnect_from_server(false)
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

	cts.C.FireConnEvent(CLIENT_EVENT_CONN_STARTED, cts)

start_over:
	cts.State.Store(CLIENT_CONN_CONNECTING)
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
		cts.remote_addr.Set(p.Addr.String())
		cts.local_addr.Set(p.LocalAddr.String())
		cts.remote_addr_p = cts.remote_addr.Get()
		cts.local_addr_p = cts.local_addr.Get()
	}

	cts.C.log.Write(cts.Sid, LOG_INFO, "Got packet stream from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])

	cts.Token.Set(cts.cfg.ClientToken)
	if cts.Token.Get() == "" { cts.Token.Set(cts.C.token) }
	cts.State.Store(CLIENT_CONN_CONNECTED)
	cts.C.FireConnEvent(CLIENT_EVENT_CONN_UPDATED, cts)

	cts.psc = &GuardedPacketStreamClient{Hodu_PacketStreamClient: psc}


	if cts.Token.Get() != "" {
		err = cts.psc.Send(MakeConnDescPacket(cts.Token.Get()))
		if err != nil {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send conn-desc(%s) to server[%d] %s - %s", cts.Token, cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index], err.Error())
			goto reconnect_to_server
		} else {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "Sending conn-desc(%s) to server[%d] %s", cts.Token, cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
		}
	}

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
			if status.Code(err) == codes.Canceled || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				goto reconnect_to_server
			} else {
				cts.C.log.Write(cts.Sid, LOG_INFO, "Failed to receive packet from %s - %s", cts.remote_addr_p, err.Error())
				goto reconnect_to_server
			}
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_STARTED:
				fallthrough
			case PACKET_KIND_ROUTE_STOPPED:
				// the server side managed to set up the route the client requested
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					err = cts.ReportPacket(RouteId(x.Route.RouteId), 0, pkt.Kind, x.Route)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle %s event(%d,%s) from %s - %s",
							pkt.Kind.String(), x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr_p, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled %s event(%d,%s) from %s",
							pkt.Kind.String(), x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr_p)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %s event from %s", pkt.Kind.String(), cts.remote_addr_p)
				}

			// PACKET_KIND_PEER_ABORTED is never sent by server to client.
			// the code here doesn't handle the event.
			case PACKET_KIND_PEER_STARTED:
				fallthrough
			case PACKET_KIND_PEER_STOPPED:
				fallthrough
			case PACKET_KIND_PEER_EOF:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportPacket(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), pkt.Kind, x.Peer)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle %s event from %s for peer(%d,%d,%s,%s) - %s",
							pkt.Kind.String(), cts.remote_addr_p, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled %s event from %s for peer(%d,%d,%s,%s)",
							pkt.Kind.String(), cts.remote_addr_p, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %s event from %s", pkt.Kind.String(), cts.remote_addr_p)
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportPacket(RouteId(x.Data.RouteId), PeerId(x.Data.PeerId), pkt.Kind, x.Data.Data)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle %s event from %s for peer(%d,%d) - %s",
							pkt.Kind.String(), cts.remote_addr_p, x.Data.RouteId, x.Data.PeerId, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled %s event from %s for peer(%d,%d)",
							pkt.Kind.String(), cts.remote_addr_p, x.Data.RouteId, x.Data.PeerId)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %s event from %s", pkt.Kind.String(), cts.remote_addr_p)
				}

			case PACKET_KIND_CONN_ERROR:
				var x *Packet_ConnErr
				var ok bool
				x, ok = pkt.U.(*Packet_ConnErr)
				if ok {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Received %s(%d, %s) event from %s", pkt.Kind.String(), x.ConnErr.ErrorId, x.ConnErr.Text, cts.remote_addr_p)
					if cts.cfg.CloseOnConnErrorEvent { goto done }
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %sevent from %s", pkt.Kind.String(), cts.remote_addr_p)
				}

			case PACKET_KIND_CONN_NOTICE:
				// the connection from the client to a peer has been established
				var x *Packet_ConnNoti
				var ok bool
				x, ok = pkt.U.(*Packet_ConnNoti)
				if ok {
					cts.C.log.Write(cts.Sid, LOG_DEBUG, "%s message '%s' received from %s", pkt.Kind.String(), x.ConnNoti.Text, cts.remote_addr_p)
					if cts.C.conn_notice_handlers != nil {
						var handler ClientConnNoticeHandler
						for _, handler = range cts.C.conn_notice_handlers {
							handler.Handle(cts, x.ConnNoti.Text)
						}
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %s packet from %s", pkt.Kind.String(), cts.remote_addr_p)
				}


			case PACKET_KIND_RPTY_START:
				fallthrough
			case PACKET_KIND_RPTY_STOP:
				fallthrough
			case PACKET_KIND_RPTY_DATA:
				fallthrough
			case PACKET_KIND_RPTY_SIZE:
				var x *Packet_RptyEvt
				var ok bool
				x, ok = pkt.U.(*Packet_RptyEvt)
				if ok {
					err = cts.HandleRptyEvent(pkt.Kind, x.RptyEvt)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle %s event for rpty(%d) from %s - %s",
							pkt.Kind.String(), x.RptyEvt.Id, cts.remote_addr_p, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled %s event for rpty(%d) from %s",
							pkt.Kind.String(), x.RptyEvt.Id, cts.remote_addr_p)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %s event from %s", pkt.Kind.String(), cts.remote_addr_p)
				}

			case PACKET_KIND_RPX_START:
				fallthrough
			case PACKET_KIND_RPX_STOP:
				fallthrough
			case PACKET_KIND_RPX_DATA:
				fallthrough
			case PACKET_KIND_RPX_EOF:
				var x *Packet_RpxEvt
				var ok bool
				x, ok = pkt.U.(*Packet_RpxEvt)
				if ok {
					err = cts.HandleRpxEvent(pkt.Kind, x.RpxEvt)
					if err != nil {
						cts.C.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle %s event for rpx(%d) from %s - %s",
							pkt.Kind.String(), x.RpxEvt.Id, cts.remote_addr_p, err.Error())
					} else {
						cts.C.log.Write(cts.Sid, LOG_DEBUG,
							"Handled %s event for rpx(%d) from %s",
							pkt.Kind.String(), x.RpxEvt.Id, cts.remote_addr_p)
					}
				} else {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid %s event from %s", pkt.Kind.String(), cts.remote_addr_p)
				}

			default:
				// do nothing. ignore the rest
		}
	}

done:
	cts.C.log.Write(cts.Sid, LOG_INFO, "Disconnected from server[%d] %s", cts.cfg.Index, cts.cfg.ServerAddrs[cts.cfg.Index])
	cts.State.Store(CLIENT_CONN_DISCONNECTED)

req_stop_and_wait_for_termination:
	//cts.RemoveClientRoutes() // this isn't needed as each task removes itself from cts upon its termination
	cts.ReqStop()

wait_for_termination:
	cts.route_wg.Wait() // wait until all route tasks are finished
	cts.C.RemoveClientConn(cts)

	cts.C.FireConnEvent(CLIENT_EVENT_CONN_STOPPED, cts)
	return

reconnect_to_server:
	// this is active disconnect. if the connection is closed from the server side,
	// CONNECTING will never be set before DISCONNECTED. See the code after the done: label above
	cts.State.Store(CLIENT_CONN_DISCONNECTING)
	cts.disconnect_from_server(true)
	cts.State.Store(CLIENT_CONN_DISCONNECTED)
	cts.C.FireConnEvent(CLIENT_EVENT_CONN_UPDATED, cts)

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
				case <-cts.C.Ctx.Done():
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

func (cts *ClientConn) ReportPacket(route_id RouteId, pts_id PeerId, packet_type PACKET_KIND, event_data interface{}) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route_id)
	}
	cts.route_mtx.Unlock()

	return r.ReportPacket(pts_id, packet_type, event_data)
}


// rpty
func (cts *ClientConn) FindClientRptyById(id uint64) *ClientRpty {
	var crp *ClientRpty
	var ok bool

	cts.rpty_mtx.Lock()
	crp, ok = cts.rpty_map[id]
	cts.rpty_mtx.Unlock()

	if !ok { crp = nil }
	return crp
}

func (cts *ClientConn) ReadRptyLoop(crp *ClientRpty, wg *sync.WaitGroup) {

	var poll_fds []unix.PollFd
	var buf [2048]byte
	var n int
	var err error

	defer wg.Done()

	cts.C.stats.rpty_sessions.Add(1)

	poll_fds = []unix.PollFd{
		unix.PollFd{Fd: int32(crp.tty.Fd()), Events: unix.POLLIN},
	}

	for {
		n, err = unix.Poll(poll_fds, -1) // -1 means wait indefinitely
		if err != nil {
			if errors.Is(err, unix.EINTR) { continue }
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to poll rpty(%d) stdout - %s", crp.id, err.Error())
			break
		}
		if n == 0 { // timed out
			continue
		}

		if (poll_fds[0].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rpty(%d) stdout", crp.id)
			break
		}

		if (poll_fds[0].Revents & unix.POLLIN) != 0 {
			n, err = crp.tty.Read(buf[:])
			if n > 0 {
				var err2 error
				err2 = cts.psc.Send(MakeRptyDataPacket(crp.id, buf[:n]))
				if err2 != nil {
					cts.C.log.Write(cts.Sid, LOG_DEBUG, "Failed to send rpty(%d) stdout to server - %s", crp.id, err2.Error())
					break
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					cts.C.log.Write(cts.Sid, LOG_DEBUG, "Failed to read rpty(%d) stdout - %s", crp.id, err.Error())
				}
				break
			}
		}
	}

	cts.psc.Send(MakeRptyStopPacket(crp.id, ""))
	cts.C.log.Write(cts.Sid, LOG_INFO, "Ending rpty(%d) read loop", crp.id)

	crp.ReqStop()
	crp.cmd.Wait()

	cts.rpty_mtx.Lock()
	delete(cts.rpty_map, crp.id)
	cts.rpty_mtx.Unlock()

	cts.C.stats.rpty_sessions.Add(-1)
}

func (cts *ClientConn) StartRpty(id uint64, wg *sync.WaitGroup) error {
	var crp *ClientRpty
	var ok bool
	var err error

	cts.rpty_mtx.Lock()
	_, ok = cts.rpty_map[id]
	if ok {
		cts.rpty_mtx.Unlock()
		return fmt.Errorf("multiple start on rpty id %d", id)
	}

	crp = &ClientRpty{ id: id }
	crp.cmd, crp.tty, err = connect_pty(cts.C.pty_shell, cts.C.pty_user)
	if err != nil {
		cts.rpty_mtx.Unlock()
		cts.psc.Send(MakeRptyStopPacket(id, err.Error()))
		return fmt.Errorf("unable to start rpty(%d) for %s(%s) - %s", id, cts.C.pty_shell, cts.C.pty_user, err.Error())
	}

	cts.rpty_map[id] = crp

	wg.Add(1)
	go cts.ReadRptyLoop(crp, wg)

	cts.rpty_mtx.Unlock()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Started rpty(%d) for %s(%s)", id, cts.C.pty_shell, cts.C.pty_user)
	return nil
}

func (cts *ClientConn) StopRpty(id uint64) error {
	var crp *ClientRpty

	crp = cts.FindClientRptyById(id)
	if crp == nil {
		return fmt.Errorf("unknown rpty id %d", id)
	}

	crp.ReqStop()
	return nil
}

func (cts *ClientConn) WriteRpty(id uint64, data []byte) error {
	var crp *ClientRpty

	crp = cts.FindClientRptyById(id)
	if crp == nil {
		return fmt.Errorf("unknown rpty id %d", id)
	}

	crp.tty.Write(data)
	return nil
}

func (cts *ClientConn) WriteRptySize(id uint64, data []byte) error {
	var crp *ClientRpty
	var flds []string

	crp = cts.FindClientRptyById(id)
	if crp == nil {
		return fmt.Errorf("unknown rpty id %d", id)
	}

	flds = strings.Split(string(data), " ")
	if len(flds) == 2 {
		var rows int
		var cols int
		rows, _ = strconv.Atoi(flds[0])
		cols, _ = strconv.Atoi(flds[1])
		pts.Setsize(crp.tty, &pts.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
	return nil
}

func (cts *ClientConn) HandleRptyEvent(packet_type PACKET_KIND, evt *RptyEvent) error {

	switch packet_type {
		case PACKET_KIND_RPTY_START:
			return cts.StartRpty(evt.Id, &cts.C.wg)

		case PACKET_KIND_RPTY_STOP:
			return cts.StopRpty(evt.Id)

		case PACKET_KIND_RPTY_DATA:
			return cts.WriteRpty(evt.Id, evt.Data)

		case PACKET_KIND_RPTY_SIZE:
			return cts.WriteRptySize(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}

// rpx
func (cts *ClientConn) FindClientRpxById(id uint64) *ClientRpx {
	var crpx *ClientRpx
	var ok bool

	cts.rpx_mtx.Lock()
	crpx, ok = cts.rpx_map[id]
	cts.rpx_mtx.Unlock()

	if !ok { crpx = nil }
	return crpx
}

func (cts *ClientConn) server_pipe_to_ws_target(crpx* ClientRpx, conn net.Conn, wg *sync.WaitGroup) {
	var buf [4096]byte
	var n int
	var err error

	defer wg.Done()

	for {
		n, err = crpx.pr.Read(buf[:])
		if n > 0 {
			var err2 error
			_, err2 = conn.Write(buf[:n])
			if err2 != nil {
				cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to write websocket for rpx(%d) - %s", crpx.id, err2.Error())
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) { break }
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to read pipe for rpx(%d) - %s", crpx.id, err.Error())
			break
		}
	}
}

func (cts *ClientConn) proxy_ws(crpx *ClientRpx, raw_req []byte, req *http.Request) (int, error) {
	var l_wg sync.WaitGroup
	var conn net.Conn
	var resp *http.Response
	var r *bufio.Reader
	var buf [4096]byte
	var n int
	var err error

	if cts.C.rpx_target_tls != nil {
		var dialer *tls.Dialer
		dialer = &tls.Dialer{
			NetDialer: &net.Dialer{},
			Config: cts.C.rpx_target_tls,
		}
		conn, err = dialer.DialContext(crpx.ctx, "tcp", cts.C.rpx_target_addr) // TODO: no hard coding
	} else {
		var dialer *net.Dialer
		dialer = &net.Dialer{}
		conn, err = dialer.DialContext(crpx.ctx, "tcp", cts.C.rpx_target_addr) // TODO: no hard coding
	}
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to dial websocket for rpx(%d) - %s", crpx.id, err.Error())
	}
	defer conn.Close()

	// TODO: make this atomic?
	crpx.ws_conn = conn

	// write the raw request line and headers as sent by the server.
	// for the upgrade request, i assume no payload.
	_, err = conn.Write(raw_req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to write websocket request for rpx(%d) - %s", crpx.id, err.Error())
	}

	r = bufio.NewReader(conn)
	resp, err = http.ReadResponse(r, req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to write websocket response for rpx(%d) - %s", crpx.id, err.Error())
	}
	defer resp.Body.Close()

	err = cts.psc.Send(MakeRpxStartPacket(crpx.id, get_http_resp_line_and_headers(resp)))
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to send rpx(%d) WebSocket headers to server - %s", crpx.id, err.Error())
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		// websock upgrade failed. let the code jump to the done
		// label to skip reading from the pipe. the server side
		// has the code to ensure no content-length. and the upgrade
		// fails, the pipe below will be pending forever as the server
		// side doesn't send data and there's no feeding to the pipe.
		return resp.StatusCode, fmt.Errorf("protocol switching failed for rpx(%d)", crpx.id)
	}

	// unlike with the normal request, the actual pipe is not read
	// until the initial switching protocol response is received.

	l_wg.Add(1)
	go cts.server_pipe_to_ws_target(crpx, conn, &l_wg)

	for {
		n, err = conn.Read(buf[:])
		if n > 0 {
			var err2 error
			err2 = cts.psc.Send(MakeRpxDataPacket(crpx.id, buf[:n]))
			if err2 != nil {
				crpx.ReqStop() // to break server_pipe_ws_target. don't care about multiple stops
				return resp.StatusCode, fmt.Errorf("failed to send rpx(%d) data to server - %s", crpx.id, err2.Error())
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				cts.psc.Send(MakeRpxEofPacket(crpx.id))
				cts.C.log.Write(cts.Sid, LOG_DEBUG, "WebSocket rpx(%d) closed by server", crpx.id)
				break
			}

			crpx.ReqStop() // to break server_pipe_ws_target. don't care about multiple stops
			return resp.StatusCode, fmt.Errorf("failed to read WebSocket rpx(%d) - %s", crpx.id, err.Error())
		}
	}

	// wait until the pipe reading(from the server side) goroutine is over
	l_wg.Wait()
	return resp.StatusCode, nil
}

func (cts *ClientConn) proxy_http(crpx *ClientRpx, req *http.Request) (int, error) {
	var tr *http.Transport
	var resp *http.Response
	var buf [4096]byte
	var n int
	var err error

	tr = &http.Transport {
		DisableKeepAlives: true, // this implementation can't support keepalive..
	}
	if cts.C.rpx_target_tls != nil {
		tr.TLSClientConfig = cts.C.rpx_target_tls
	}

	resp, err = tr.RoundTrip(req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to send rpx(%d) request - %s", crpx.id, err.Error())
	}
	defer resp.Body.Close()

	err = cts.psc.Send(MakeRpxStartPacket(crpx.id, get_http_resp_line_and_headers(resp)))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("failed to send rpx(%d) status and headers to server - %s", crpx.id, err.Error())
	}

	for {
		n, err = resp.Body.Read(buf[:])
		if n > 0 {
			var err2 error
			err2 = cts.psc.Send(MakeRpxDataPacket(crpx.id, buf[:n]))
			if err2 != nil {
				return resp.StatusCode, fmt.Errorf("failed to send rpx(%d) data to server - %s", crpx.id, err2.Error())
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return resp.StatusCode, fmt.Errorf("failed to read response body for rpx(%d) - %s", crpx.id, err.Error())
		}
	}

	return resp.StatusCode, nil
}

func (cts *ClientConn) RpxLoop(crpx *ClientRpx, data []byte, wg *sync.WaitGroup) {
	var start_time time.Time
	var time_taken time.Duration
	var sc *bufio.Scanner
	var line string
	var flds []string
	var req_meth string
	var req_path string
	var req_proto string
	var raw_req bytes.Buffer
	var status_code int
	var req *http.Request
	var err error

	defer wg.Done()

	start_time = time.Now()

	sc = bufio.NewScanner(bytes.NewReader(data))
	sc.Scan()
	line = sc.Text()

	flds = strings.Fields(line)
	if (len(flds) < 3) {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid request line for rpx(%d) - %s", crpx.id, line)
		goto done
	}

// TODO: handle trailers...
	req_meth = flds[0]
	req_path = flds[1]
	req_proto = flds[2]

	raw_req.WriteString(line)
	raw_req.WriteString("\r\n")

	// create a request assuming it's a normal http request
	req, err = http.NewRequestWithContext(crpx.ctx, req_meth, cts.C.rpx_target_url + req_path, crpx.pr)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "failed to create request for rpx(%d) - %s", crpx.id, err.Error())
		goto done
	}

	for sc.Scan() {
		line = sc.Text()
		if line == "" { break }
		flds = strings.SplitN(line, ":", 2)
		if len(flds) == 2 {
			var k string
			var v string
			k = strings.TrimSpace(flds[0])
			v = strings.TrimSpace(flds[1])
			req.Header.Add(k, v)

			if strings.EqualFold(flds[0], "Host") {
				// a normal http client would set HOst to be the target address.
				// the raw header is coming from the server. so it's different
				// from the host it's supposed to be. correct it to the right value.
				fmt.Fprintf(&raw_req, "%s: %s\r\n", flds[0], req.Host)
			} else {
				raw_req.WriteString(line)
				raw_req.WriteString("\r\n")
			}
		}
	}
	err = sc.Err()
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "failed to parse request for rpx(%d) - %s", crpx.id, err.Error())
		goto done
	}
	raw_req.WriteString("\r\n")

	if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade") {
		// websocket
		status_code, err = cts.proxy_ws(crpx, raw_req.Bytes(), req)
	} else {
		// normal http
		status_code, err = cts.proxy_http(crpx, req)
	}

	time_taken = time.Since(start_time)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "rpx(%d), - %s %s %s %d %.9f - failed to proxy - %s", crpx.id, req_meth, req_path, req_proto, status_code, time_taken.Seconds(), err.Error())
		goto done
	} else {
		cts.C.log.Write(cts.Sid, LOG_INFO, "rpx(%d) - %s %s %s %d %.9f", crpx.id, req_meth, req_path, req_proto, status_code, time_taken.Seconds())
	}

done:
	err = cts.psc.Send(MakeRpxStopPacket(crpx.id))
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send rpx(%d) stp to server - %s", crpx.id, err.Error())
	}
	cts.C.log.Write(cts.Sid, LOG_INFO, "Ending rpx(%d) read loop", crpx.id)

	crpx.ReqStop()

	cts.rpx_mtx.Lock()
	delete(cts.rpx_map, crpx.id)
	cts.rpx_mtx.Unlock()
	cts.C.stats.rpx_sessions.Add(-1)
}

func (cts *ClientConn) StartRpx(id uint64, data []byte, wg *sync.WaitGroup) error {
	var crpx *ClientRpx
	var ok bool

	cts.rpx_mtx.Lock()
	_, ok = cts.rpx_map[id]
	if ok {
		cts.rpx_mtx.Unlock()
		return fmt.Errorf("multiple start on rpx id %d", id)
	}
	crpx = &ClientRpx{ id: id }
	cts.rpx_map[id] = crpx

	// i want the pipe to be created before the goroutine is started
	// so that the WriteRpx() can write to the pipe. i protect pipe creation
	// and context creation with a mutex
	crpx.pr, crpx.pw = io.Pipe()
	crpx.ctx, crpx.cancel = context.WithCancel(cts.C.Ctx)

	cts.rpx_mtx.Unlock()
	cts.C.stats.rpx_sessions.Add(1)

	wg.Add(1)
	go cts.RpxLoop(crpx, data, wg)

	return nil
}

func (cts *ClientConn) StopRpx(id uint64) error {
	var crpx *ClientRpx

	crpx = cts.FindClientRpxById(id)
	if crpx == nil {
		return fmt.Errorf("unknown rpx id %d", id)
	}

	crpx.ReqStop()
	return nil
}

func (cts *ClientConn) WriteRpx(id uint64, data []byte) error {
	var crpx *ClientRpx
	var err error

	crpx = cts.FindClientRpxById(id)
	if crpx == nil {
		return fmt.Errorf("unknown rpx id %d", id)
	}

// TODO: may have to write it in a goroutine to avoid blocking?
	_, err = crpx.pw.Write(data)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to write rpx(%d) data - %s", id, err.Error())
		return err
	}
	return nil
}

func (cts *ClientConn) EofRpx(id uint64, data []byte) error {
	var crpx *ClientRpx

	crpx = cts.FindClientRpxById(id)
	if crpx == nil {
		return fmt.Errorf("unknown rpx id %d", id)
	}

	// close the writing end only. leave the reading end untouched
	crpx.pw.Close()

	return nil
}

func (cts *ClientConn) HandleRpxEvent(packet_type PACKET_KIND, evt *RpxEvent) error {
	switch packet_type {
		case PACKET_KIND_RPX_START:
			return cts.StartRpx(evt.Id, evt.Data, &cts.C.wg)

		case PACKET_KIND_RPX_STOP:
			return cts.StopRpx(evt.Id)

		case PACKET_KIND_RPX_DATA:
			return cts.WriteRpx(evt.Id, evt.Data)

		case PACKET_KIND_RPX_EOF:
			return cts.EofRpx(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}

// --------------------------------------------------------------------

func (m ClientPeerConnMap) get_sorted_keys() []PeerId {
	var ks []PeerId
	var peer *ClientPeerConn

	ks = make([]PeerId, 0, len(m))
	for _, peer = range m {
		ks = append(ks, peer.conn_id)
	}
	slices.Sort(ks)
	return ks
}

func (m ClientRouteMap) get_sorted_keys() []RouteId {
	var ks []RouteId
	var route *ClientRoute

	ks = make([]RouteId, 0, len(m))
	for _, route = range m {
		ks = append(ks, route.Id)
	}
	slices.Sort(ks)
	return ks
}

func (m ClientConnMap) get_sorted_keys() []ConnId {
	var ks []ConnId
	var cts *ClientConn

	ks = make([]ConnId, 0, len(m))
	for _, cts = range m {
		ks = append(ks, cts.Id)
	}
	slices.Sort(ks)
	return ks
}

// --------------------------------------------------------------------

type client_ctl_log_writer struct {
	cli *Client
	depth int
}

func (hlw *client_ctl_log_writer) Write(p []byte) (n int, err error) {
	// the standard http.Server always requires *log.Logger
	// use this iowriter to create a logger to pass it to the http server.
	hlw.cli.log.WriteWithCallDepth("", LOG_INFO, hlw.depth, string(p))
	return len(p), nil
}

type ClientHttpHandler interface {
	Identity() string
	Cors(req *http.Request) bool
	Authenticate(req *http.Request) (int, string)
	ServeHTTP (w http.ResponseWriter, req *http.Request) (int, error)
}

type ClientWebsocketHandler interface {
	Identity() string
	ServeWebsocket(ws *websocket.Conn) (int, error)
}


func (c *Client) WrapHttpHandler(handler ClientHttpHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var status_code int
		var err error
		var start_time time.Time
		var time_taken time.Duration

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
			var realm string

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
		time_taken = time.Since(start_time) //time.Now().Sub(start_time)

		if status_code > 0 {
			if err != nil {
				c.log.Write(handler.Identity(), LOG_INFO, "[%s] %s %s %d %.9f - %s", req.RemoteAddr, req.Method, req.RequestURI, status_code, time_taken.Seconds(), err.Error())
			} else {
				c.log.Write(handler.Identity(), LOG_INFO, "[%s] %s %s %d %.9f", req.RemoteAddr, req.Method, req.RequestURI, status_code, time_taken.Seconds())
			}
		}
	})
}

func (c *Client) SafeWrapWebsocketHandler(handler websocket.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") ||
		   !strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade") {
			var status_code int
			status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
			c.log.Write("", LOG_INFO, "[%s] %s %s %d[non-websocket]", req.RemoteAddr, req.Method, req.RequestURI, status_code)
			return
		}
		handler.ServeHTTP(w, req)
	})
}

func (c *Client) WrapWebsocketHandler(handler ClientWebsocketHandler) websocket.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		var status_code int
		var err error
		var start_time time.Time
		var time_taken time.Duration
		var req *http.Request

		req = ws.Request()
		c.log.Write(handler.Identity(), LOG_INFO, "[%s] %s %s [ws]", req.RemoteAddr, req.Method, req.RequestURI)

		start_time = time.Now()
		status_code, err = handler.ServeWebsocket(ws)
		time_taken = time.Since(start_time) // time.Now().Sub(start_time)

		if status_code > 0 {
			if err != nil {
				c.log.Write(handler.Identity(), LOG_INFO, "[%s] %s %s [ws] %d %.9f - %s", req.RemoteAddr, req.Method, req.RequestURI, status_code, time_taken.Seconds(), err.Error())
			} else {
				c.log.Write(handler.Identity(), LOG_INFO, "[%s] %s %s [ws] %d %.9f", req.RemoteAddr, req.Method, req.RequestURI, status_code, time_taken.Seconds())
			}
		}
	})
}

func NewClient(ctx context.Context, name string, logger Logger, cfg *ClientConfig) *Client {
	var c Client
	var i int
	var hs_log *log.Logger

	c.name = name
	c.log = logger
	c.token = cfg.Token
	c.Ctx, c.CtxCancel = context.WithCancel(ctx)
	c.ext_svcs = make([]Service, 0, 1)
	c.route_list = list.New()
	c.ptc_list = list.New()
	c.ptc_tmout = cfg.PeerConnTmout
	c.ptc_limit = cfg.PeerConnMax
	c.cts_limit = cfg.RpcConnMax
	c.cts_next_id = 1
	c.cts_map = make(ClientConnMap)
	c.stop_req.Store(false)
	c.stop_chan = make(chan bool, 8)
	c.bulletin = NewBulletin[*ClientEvent](&c, 1024)

	c.rpc_tls = cfg.RpcTls
	c.rpx_target_addr = cfg.RpxTargetAddr
	c.rpx_target_addr_org = cfg.RpxTargetAddr
	c.rpx_target_tls = cfg.RpxTargetTls
	if c.rpx_target_tls != nil {
		c.rpx_target_url = "https://" + c.rpx_target_addr
		if !strings.Contains(cfg.RpxTargetAddr, ":") {
			c.rpx_target_addr = cfg.RpxTargetAddr + ":443"
		}
	} else {
		c.rpx_target_url = "http://" + c.rpx_target_addr
		if !strings.Contains(cfg.RpxTargetAddr, ":") {
			c.rpx_target_addr = cfg.RpxTargetAddr + ":80"
		}
	}

	c.ctl_auth = cfg.CtlAuth
	c.ctl_tls = cfg.CtlTls
	c.ctl_prefix = cfg.CtlPrefix
	c.ctl_cors = cfg.CtlCors
	c.ctl_mux = http.NewServeMux()
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns",
		c.WrapHttpHandler(&client_ctl_client_conns{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}",
		c.WrapHttpHandler(&client_ctl_client_conns_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes",
		c.WrapHttpHandler(&client_ctl_client_conns_id_routes{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes/{route_id}",
		c.WrapHttpHandler(&client_ctl_client_conns_id_routes_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes-spsp/{port_id}",
		c.WrapHttpHandler(&client_ctl_client_conns_id_routes_spsp{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes/{route_id}/peers",
		c.WrapHttpHandler(&client_ctl_client_conns_id_routes_id_peers{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/routes/{route_id}/peers/{peer_id}",
		c.WrapHttpHandler(&client_ctl_client_conns_id_routes_id_peers_id{client_ctl{c: &c, id: HS_ID_CTL}}))

	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-conns/{conn_id}/peers",
		c.WrapHttpHandler(&client_ctl_client_conns_id_peers{client_ctl{c: &c, id: HS_ID_CTL}}))

	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-routes",
		c.WrapHttpHandler(&client_ctl_client_routes{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/client-peers",
		c.WrapHttpHandler(&client_ctl_client_peers{client_ctl{c: &c, id: HS_ID_CTL}}))

	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/notices",
		c.WrapHttpHandler(&client_ctl_notices{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/notices/{conn_id}",
		c.WrapHttpHandler(&client_ctl_notices_id{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/stats",
		c.WrapHttpHandler(&client_ctl_stats{client_ctl{c: &c, id: HS_ID_CTL}}))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/token",
		c.WrapHttpHandler(&client_ctl_token{client_ctl{c: &c, id: HS_ID_CTL}}))

// TODO: make this optional. add this endpoint only if it's enabled...
	c.promreg = prometheus.NewRegistry()
	c.promreg.MustRegister(prometheus.NewGoCollector())
	c.promreg.MustRegister(NewClientCollector(&c))
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl/metrics",
		promhttp.HandlerFor(c.promreg, promhttp.HandlerOpts{ EnableOpenMetrics: true }))

	c.ctl_mux.Handle("/_ctl/events",
		c.SafeWrapWebsocketHandler(c.WrapWebsocketHandler(&client_ctl_ws{client_ctl{c: &c, id: HS_ID_CTL}})))

	c.ctl_mux.Handle("/_pty/ws",
		c.SafeWrapWebsocketHandler(c.WrapWebsocketHandler(&client_pty_ws{C: &c, Id: HS_ID_CTL})))

	c.ctl_mux.Handle("/_pty/xterm.js",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "xterm.js"}))
	c.ctl_mux.Handle("/_pty/xterm.js/",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "_forbidden"}))
	c.ctl_mux.Handle("/_pty/xterm-addon-fit.js",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "xterm-addon-fit.js"}))
	c.ctl_mux.Handle("/_pty/xterm-addon-fit.js/",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "_forbidden"}))
	c.ctl_mux.Handle("/_pty/xterm.css",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "xterm.css"}))
	c.ctl_mux.Handle("/_pty/xterm.css/",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "_forbidden"}))
	c.ctl_mux.Handle("/_pty/xterm.html",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "xterm.html"}))
	c.ctl_mux.Handle("/_pty/xterm.html/", // without this forbidden, /_pty/xterm.js/ access resulted in xterm.html.
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "_forbidden"}))
	c.ctl_mux.Handle("/_pty/",
		c.WrapHttpHandler(&client_pty_xterm_file{client_ctl: client_ctl{c: &c, id: HS_ID_CTL}, file: "_redir:xterm.html"}))

	c.ctl_addr = make([]string, len(cfg.CtlAddrs))
	c.ctl = make([]*http.Server, len(cfg.CtlAddrs))
	copy(c.ctl_addr, cfg.CtlAddrs)

	hs_log = log.New(&client_ctl_log_writer{cli: &c, depth: 0}, "", 0)

	for i = 0; i < len(cfg.CtlAddrs); i++ {
		c.ctl[i] = &http.Server{
			Addr: cfg.CtlAddrs[i],
			Handler: c.ctl_mux,
			TLSConfig: c.ctl_tls.Clone(),
			ErrorLog: hs_log,
			// TODO: more settings
		}
	}

	c.stats.conns.Store(0)
	c.stats.routes.Store(0)
	c.stats.peers.Store(0)
	c.stats.pty_sessions.Store(0)
	c.stats.rpty_sessions.Store(0)
	c.stats.rpx_sessions.Store(0)

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

func (c *Client) FindClientRouteByServerPeerSvcPortId(conn_id ConnId, port_id PortId) *ClientRoute {
	var cts *ClientConn
	var ok bool

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	cts, ok = c.cts_map[conn_id]
	if !ok { return nil }

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

func (c *Client) FindClientConnByIdStr(conn_id string) (*ClientConn, error) {
	var conn_nid uint64
	var cts *ClientConn
	var err error

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error())
		//cts = c.FindClientConnByToken(conn_id) // if not numeric, attempt to use it as a token
		//if cts == nil { return nil, fmt.Errorf("non-existent connection token '%s'", conn_id) }
	} else {
		cts = c.FindClientConnById(ConnId(conn_nid))
		if cts == nil { return nil, fmt.Errorf("non-existent connection id %d", conn_nid) }
	}

	return cts, nil
}

func (c *Client) FindClientRouteByIdStr(conn_id string, route_id string) (*ClientRoute, error) {
	var r *ClientRoute
	var conn_nid uint64
	var route_nid uint64
	var err error

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error()) }

	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid route id %s - %s", route_id, err.Error()) }

	r = c.FindClientRouteById(ConnId(conn_nid), RouteId(route_nid))
	if r == nil { return nil, fmt.Errorf("route(%d,%d) not found", conn_nid, route_nid) }

	return r, nil
}

func (c *Client) FindClientRouteByServerPeerSvcPortIdStr(conn_id string, port_id string) (*ClientRoute, error) {
	var conn_nid uint64
	var port_nid uint64
	var r *ClientRoute
	var err error

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error()) }

	port_nid, err = strconv.ParseUint(port_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid port id %s - %s", port_id, err.Error()) }

	r = c.FindClientRouteByServerPeerSvcPortId(ConnId(conn_nid), PortId(port_nid))
	if r == nil { return nil, fmt.Errorf("non-existent route(%d,%d)", conn_nid, port_nid) }

	return r, nil
}

func (c *Client) FindClientPeerConnByIdStr(conn_id string, route_id string, peer_id string) (*ClientPeerConn, error) {
	var p *ClientPeerConn
	var conn_nid uint64
	var route_nid uint64
	var peer_nid uint64
	var err error

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error()) }

	route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid route id %s - %s", route_id, err.Error()) }

	peer_nid, err = strconv.ParseUint(peer_id, 10, int(unsafe.Sizeof(PeerId(0)) * 8))
	if err != nil { return nil, fmt.Errorf("invalid peer id %s - %s", peer_id, err.Error()) }

	p = c.FindClientPeerConnById(ConnId(conn_nid), RouteId(route_nid), PeerId(peer_nid))
	if p == nil { return nil, fmt.Errorf("peer(%d,%d,%d) not found", conn_nid, route_nid, peer_nid) }

	return p, nil
}

func (c *Client) ReqStop() {
	if c.stop_req.CompareAndSwap(false, true) {
		var cts *ClientConn
		var ctl *http.Server

		c.bulletin.Block()

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

func (c *Client) SetXtermHtml(html string) {
	c.xterm_html = html
}

func (c *Client) GetXtermHtml() string {
	return c.xterm_html
}

func (c *Client) SetPtyUser(user string) {
	c.pty_user = user
}

func (c *Client) GetPtyUser() string {
	return c.pty_user
}

func (c *Client) SetPtyShell(user string) {
	c.pty_shell = user
}

func (c *Client) GetPtyShell() string {
	return c.pty_shell
}

func (c *Client) run_single_ctl_server(i int, cs *http.Server, wg *sync.WaitGroup) {
	var l net.Listener
	var err error

	defer wg.Done()

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
}

func (c *Client) RunCtlTask(wg *sync.WaitGroup) {
	var ctl *http.Server
	var idx int
	var l_wg sync.WaitGroup

	defer wg.Done()

	for idx, ctl = range c.ctl {
		l_wg.Add(1)
		go c.run_single_ctl_server(idx, ctl, &l_wg)
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

	c.wg.Add(1)
	go c.bulletin.RunTask(&c.wg)

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
	c.bulletin.ReqStop()
	for _, ext_svc = range c.ext_svcs {
		ext_svc.StopServices()
	}
}

func (c *Client) FixServices() {
	c.log.Rotate()
}

func (c *Client) WaitForTermination() {
	c.wg.Wait()
	c.log.Write("", LOG_INFO, "End of service")
}

func (c *Client) WriteLog(id string, level LogLevel, fmtstr string, args ...interface{}) {
	c.log.Write(id, level, fmtstr, args...)
}

func (c *Client) SetConnNoticeHandlers(handlers []ClientConnNoticeHandler) {
	c.conn_notice_handlers = handlers
}

func (c *Client) SetRoutePersister(persister ClientRoutePersister) {
	c.route_persister = persister
}

func (c *Client) AddCtlHandler(path string, handler ClientHttpHandler) {
	// parked under /_ctl
	c.ctl_mux.Handle(c.ctl_prefix + "/_ctl" + path, c.WrapHttpHandler(handler))
}

func (c *Client) AddCtlRootHandler(path string, handler ClientHttpHandler) {
	// parked at the root level. must avoid conflicting path
	c.ctl_mux.Handle(c.ctl_prefix + path, c.WrapHttpHandler(handler))
}

func (c *Client) AddCtlMetricsCollector(col prometheus.Collector) error {
	return c.promreg.Register(col)
}

func (c *Client) RemoveCtlMetricsCollector(col prometheus.Collector) bool {
	return c.promreg.Unregister(col)
}

func (c *Client) FireConnEvent(event_kind ClientEventKind, cts *ClientConn) {
	if event_kind == CLIENT_EVENT_CONN_STOPPED {
		c.bulletin.Enqueue(
			&ClientEvent{
				Kind: event_kind,
				Data: &json_out_client_conn{ CId: cts.Id },
			},
		)
	} else {
		c.bulletin.Enqueue(
			&ClientEvent{
				Kind: event_kind,
				Data: &json_out_client_conn{
					CId: cts.Id,
					ReqServerAddrs: cts.cfg.ServerAddrs,
					CurrentServerIndex: cts.cfg.Index,
					ServerAddr: cts.remote_addr.Get(),
					ClientAddr: cts.local_addr.Get(),
					ClientToken: cts.Token.Get(),
					CreatedMilli: cts.Created.UnixMilli(),
				},
			},
		)
	}
}

func (c *Client) FireRouteEvent(event_kind ClientEventKind, r *ClientRoute) {
	if event_kind == CLIENT_EVENT_ROUTE_STOPPED {
		c.bulletin.Enqueue(
			&ClientEvent{
				Kind: event_kind,
				Data: &json_out_client_route_id {
					CId: r.cts.Id, RId: r.Id,
				},
			},
		)
	} else {
		var lftsta time.Time
		var lftdur time.Duration

		lftsta, lftdur = r.GetLifetimeInfo()
		c.bulletin.Enqueue(
			&ClientEvent{
				Kind: event_kind,
				Data: &json_out_client_route{
					CId: r.cts.Id,
					RId: r.Id,
					ClientPeerAddr: r.PeerAddr,
					ClientPeerName: r.PeerName,
					ServerPeerSvcAddr: r.ServerPeerSvcAddr.Get(),
					ServerPeerSvcNet: r.ServerPeerSvcNet.Get(),
					ServerPeerOption: r.ServerPeerOption.String(),
					Lifetime: DurationToSecString(lftdur),
					LifetimeStartMilli: lftsta.UnixMilli(),
					CreatedMilli: r.Created.UnixMilli(),
				},
			},
		)
	}
}

func (c *Client) FirePeerEvent(event_kind ClientEventKind, ptc *ClientPeerConn) {
	if event_kind == CLIENT_EVENT_PEER_STOPPED {
		c.bulletin.Enqueue(
			&ClientEvent{
				Kind: event_kind,
				Data: &json_out_client_peer_id{
					CId: ptc.route.cts.Id,
					RId: ptc.route.Id,
					PId: ptc.conn_id,
				},
			},
		)

	} else {
		var r *ClientRoute

		r = ptc.route
		c.bulletin.Enqueue(
			&ClientEvent{
				Kind: event_kind,
				Data: &json_out_client_peer{
					CId: r.cts.Id,
					RId: r.Id,
					PId: ptc.conn_id,
					ClientPeerAddr: ptc.conn.RemoteAddr().String(),
					ClientLocalAddr: ptc.conn.LocalAddr().String(),
					ServerPeerAddr: ptc.pts_raddr,
					ServerLocalAddr: ptc.pts_laddr,
					CreatedMilli: ptc.Created.UnixMilli(),
				},
			},
		)
	}
}
