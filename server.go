package hodu

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "io"
import "log"
import "net"
import "net/http"
import "net/netip"
import "slices"
import "strconv"
import "sync"
import "sync/atomic"
import "time"
import "unsafe"

import "golang.org/x/net/websocket"
import "google.golang.org/grpc"
import "google.golang.org/grpc/credentials"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/stats"
import "github.com/prometheus/client_golang/prometheus"
import "github.com/prometheus/client_golang/prometheus/promhttp"

const PTS_LIMIT int = 16384
const CTS_LIMIT int = 16384

type PortId uint16
const PORT_ID_MARKER string = "_"
const HS_ID_CTL string = "ctl"
const HS_ID_WPX string = "wpx"
const HS_ID_PXY string = "pxy"
const HS_ID_PXY_WS string = "pxy-ws"

type ServerConnMapByAddr map[net.Addr]*ServerConn
type ServerConnMapByClientToken map[string]*ServerConn
type ServerConnMap map[ConnId]*ServerConn
type ServerRouteMap map[RouteId]*ServerRoute
type ServerPeerConnMap map[PeerId]*ServerPeerConn
type ServerSvcPortMap map[PortId]ConnRouteId

type ServerWpxResponseTransformer func(r *ServerRouteProxyInfo, resp *http.Response) io.Reader
type ServerWpxForeignPortProxyMaker func(wpx_type string, port_id string) (*ServerRouteProxyInfo, error)

type ServerConnNoticeHandler interface {
	Handle(cts* ServerConn, text string)
}

type ServerConfig struct {
	RpcAddrs []string
	RpcTls *tls.Config
	RpcMaxConns int
	MaxPeers int

	CtlAddrs []string
	CtlTls *tls.Config
	CtlPrefix string
	CtlAuth *HttpAuthConfig
	CtlCors bool

	PxyAddrs []string
	PxyTls *tls.Config

	WpxAddrs []string
	WpxTls *tls.Config
}

type Server struct {
	UnimplementedHoduServer
	Named

	Cfg              *ServerConfig
	Ctx              context.Context
	CtxCancel        context.CancelFunc

	wg               sync.WaitGroup
	stop_req         atomic.Bool
	stop_chan        chan bool

	ext_mtx          sync.Mutex
	ext_svcs         []Service

	pxy_ws           *server_proxy_ssh_ws
	pxy_mux          *http.ServeMux
	pxy              []*http.Server // proxy server

	wpx_ws           *server_proxy_ssh_ws
	wpx_mux          *http.ServeMux
	wpx              []*http.Server // proxy server than handles http/https only

	ctl_mux          *http.ServeMux
	ctl              []*http.Server // control server

	rpc              []*net.TCPListener // main listener for grpc
	rpc_wg           sync.WaitGroup
	rpc_svr          *grpc.Server

	pts_limit        int // global pts limit
	cts_limit        int
	cts_next_id      ConnId
	cts_mtx          sync.Mutex
	cts_map          ServerConnMap
	cts_map_by_addr  ServerConnMapByAddr
	cts_map_by_token ServerConnMapByClientToken
	cts_wg           sync.WaitGroup

	log              Logger
	conn_notice      ServerConnNoticeHandler

	svc_port_mtx     sync.Mutex
	svc_port_map     ServerSvcPortMap

	promreg *prometheus.Registry
	stats struct {
		conns atomic.Int64
		routes atomic.Int64
		peers atomic.Int64
		ssh_proxy_sessions atomic.Int64
	}

	wpx_resp_tf     ServerWpxResponseTransformer
	wpx_foreign_port_proxy_maker ServerWpxForeignPortProxyMaker
	xterm_html      string
}

// connection from client.
// client connect to the server, the server accept it, and makes a tunnel request
type ServerConn struct {
	S            *Server
	Id            ConnId
	Sid           string // for logging
	ClientToken   string // provided by client

	RemoteAddr    net.Addr // client address that created this structure
	LocalAddr     net.Addr // local address that the client is connected to
	pss          *GuardedPacketStreamServer

	route_mtx     sync.Mutex
	route_map     ServerRouteMap
	route_wg      sync.WaitGroup

	wg            sync.WaitGroup
	stop_req      atomic.Bool
	stop_chan     chan bool
}

type ServerRoute struct {
	Cts        *ServerConn
	Id          RouteId

	svc_l      *net.TCPListener
	SvcAddr    *net.TCPAddr // actual listening address
	SvcReqAddr  string
	SvcPermNet  netip.Prefix // network from which access is allowed
	SvcOption   RouteOption

	PtcAddr     string
	PtcName     string

	pts_mtx     sync.Mutex
	pts_map     ServerPeerConnMap
	pts_limit   int
	pts_next_id PeerId
	pts_wg      sync.WaitGroup
	stop_req    atomic.Bool
}

type GuardedPacketStreamServer struct {
	mtx sync.Mutex
	//pss Hodu_PacketStreamServer
	Hodu_PacketStreamServer // let's embed it to avoid reimplement Recv() and Context()
}

// ------------------------------------

func (g *GuardedPacketStreamServer) Send(data *Packet) error {
	// while Recv() on a stream is called from the same gorountine all the time,
	// Send() is called from multiple places. let's guard it as grpc-go
	// doesn't provide concurrency safety in this case.
	// https://github.com/grpc/grpc-go/blob/master/Documentation/concurrency.md
	g.mtx.Lock()
	defer g.mtx.Unlock()
	return g.Hodu_PacketStreamServer.Send(data)
}

/*
func (g *GuardedPacketStreamServer) Recv() (*Packet, error) {
	return g.pss.Recv()
}

func (g *GuardedPacketStreamServer) Context() context.Context {
	return g.pss.Context()
}*/

// ------------------------------------

func NewServerRoute(cts *ServerConn, id RouteId, option RouteOption, ptc_addr string, ptc_name string, svc_requested_addr string, svc_permitted_net string) (*ServerRoute, error) {
	var r ServerRoute
	var l *net.TCPListener
	var svcaddr *net.TCPAddr
	var svcnet netip.Prefix
	var err error

	if svc_permitted_net != "" {
		// parse the permitted network before creating a listener.
		// the listener opened doesn't have to be closed when parsing fails.
		svcnet, err = netip.ParsePrefix(svc_permitted_net)
		if err != nil {
			return nil , err
		}
	}

	l, svcaddr, err = cts.make_route_listener(id, option, svc_requested_addr)
	if err != nil { return nil, err }

	if svc_permitted_net == "" {
		if svcaddr.IP.To4() != nil {
			svcnet = IPV4_PREFIX_ZERO
		} else {
			svcnet = IPV6_PREFIX_ZERO
		}
	}

	r.Cts = cts
	r.Id = id
	r.svc_l = l
	r.SvcAddr = svcaddr
	r.SvcReqAddr = svc_requested_addr
	r.SvcPermNet = svcnet
	r.SvcOption = option

	r.PtcAddr = ptc_addr
	r.PtcName = ptc_name
	r.pts_limit = PTS_LIMIT
	r.pts_map = make(ServerPeerConnMap)
	r.pts_next_id = 1
	r.stop_req.Store(false)

	return &r, nil
}

func (r *ServerRoute) AddNewServerPeerConn(c *net.TCPConn) (*ServerPeerConn, error) {
	var pts *ServerPeerConn
	var ok bool
	var start_id PeerId
	var assigned_id PeerId

	r.pts_mtx.Lock()
	defer r.pts_mtx.Unlock()

	if len(r.pts_map) >= r.pts_limit {
		return nil, fmt.Errorf("peer-to-server connection table full")
	}

	start_id = r.pts_next_id
	for {
		_, ok = r.pts_map[r.pts_next_id]
		if !ok {
			assigned_id = r.pts_next_id
			r.pts_next_id++
			if r.pts_next_id == 0 { r.pts_next_id++ }
			break
		}
		r.pts_next_id++
		if r.pts_next_id == 0 { r.pts_next_id++ }
		if r.pts_next_id == start_id {
			// unlikely to happen but it cycled through the whole range.
			return nil, fmt.Errorf("failed to assign peer-to-server connection id")
		}
	}

	pts = NewServerPeerConn(r, c, assigned_id)
	r.pts_map[pts.conn_id] = pts
	r.Cts.S.stats.peers.Add(1)

	return pts, nil
}

func (r *ServerRoute) RemoveServerPeerConn(pts *ServerPeerConn) {
	r.pts_mtx.Lock()
	delete(r.pts_map, pts.conn_id)
	r.Cts.S.stats.peers.Add(-1)
	r.pts_mtx.Unlock()
	r.Cts.S.log.Write(r.Cts.Sid, LOG_DEBUG, "Removed server-side peer connection %s from route(%d)", pts.conn.RemoteAddr().String(), r.Id)
}

func (r *ServerRoute) RunTask(wg *sync.WaitGroup) {
	var err error
	var conn *net.TCPConn
	var pts *ServerPeerConn
	var raddr *net.TCPAddr
	var iaddr netip.Addr

	defer wg.Done()

	for {
		conn, err = r.svc_l.AcceptTCP() // this call is blocking...
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				r.Cts.S.log.Write(r.Cts.Sid, LOG_INFO, "Server-side peer listener closed on route(%d)", r.Id)
			} else {
				r.Cts.S.log.Write(r.Cts.Sid, LOG_INFO, "Server-side peer listener error on route(%d) - %s", r.Id, err.Error())
			}
			break
		}

		raddr = conn.RemoteAddr().(*net.TCPAddr)
		iaddr, _ = netip.AddrFromSlice(raddr.IP)

		if !r.SvcPermNet.Contains(iaddr) {
			r.Cts.S.log.Write(r.Cts.Sid, LOG_DEBUG, "Rejected server-side peer %s to route(%d) - allowed range %v", raddr.String(), r.Id, r.SvcPermNet)
			conn.Close()
		}

		if r.Cts.S.pts_limit > 0 && int(r.Cts.S.stats.peers.Load()) >= r.Cts.S.pts_limit {
			r.Cts.S.log.Write(r.Cts.Sid, LOG_DEBUG, "Rejected server-side peer %s to route(%d) - allowed max %d", raddr.String(), r.Id, r.Cts.S.pts_limit)
			conn.Close()
		}

		pts, err = r.AddNewServerPeerConn(conn)
		if err != nil {
			r.Cts.S.log.Write(r.Cts.Sid, LOG_ERROR, "Failed to add server-side peer %s to route(%d) - %s", r.Id, raddr.String(), r.Id, err.Error())
			conn.Close()
		} else {
			r.Cts.S.log.Write(r.Cts.Sid, LOG_DEBUG, "Added server-side peer %s to route(%d)", raddr.String(), r.Id)
			r.pts_wg.Add(1)
			go pts.RunTask(&r.pts_wg)
		}
	}

	r.ReqStop()

	r.pts_wg.Wait()
	r.Cts.S.log.Write(r.Cts.Sid, LOG_DEBUG, "All service-side peer handlers ended on route(%d)", r.Id)

	r.Cts.RemoveServerRoute(r) // final phase...
}

func (r *ServerRoute) ReqStop() {
	if r.stop_req.CompareAndSwap(false, true) {
		var pts *ServerPeerConn

		r.pts_mtx.Lock()
		for _, pts = range r.pts_map { pts.ReqStop() }
		r.pts_mtx.Unlock()

		r.svc_l.Close()
	}
}

func (r *ServerRoute) ReqStopAllServerPeerConns() {
	var c *ServerPeerConn

	r.pts_mtx.Lock()
	for _, c = range r.pts_map { c.ReqStop() }
	r.pts_mtx.Unlock()
}

func (r *ServerRoute) FindServerPeerConnById(peer_id PeerId) *ServerPeerConn {
	var c *ServerPeerConn
	var ok bool

	r.pts_mtx.Lock()
	defer r.pts_mtx.Unlock()

	c, ok = r.pts_map[peer_id]
	if !ok { return nil }

	return c
}

func (r *ServerRoute) ReportEvent(pts_id PeerId, event_type PACKET_KIND, event_data interface{}) error {
	var spc *ServerPeerConn
	var ok bool

	r.pts_mtx.Lock()
	spc, ok = r.pts_map[pts_id]
	if !ok {
		r.pts_mtx.Unlock()
		return fmt.Errorf("non-existent peer id - %d", pts_id)
	}
	r.pts_mtx.Unlock()

	return spc.ReportEvent(event_type, event_data)
}
// ------------------------------------

func (cts *ServerConn) make_route_listener(id RouteId, option RouteOption, svc_requested_addr string) (*net.TCPListener, *net.TCPAddr, error) {
	var l *net.TCPListener
	var svcaddr *net.TCPAddr
	var nw string
	var prev_cri ConnRouteId
	var ok bool
	var err error

	if svc_requested_addr != "" {
		var ap netip.AddrPort

		ap, err = netip.ParseAddrPort(svc_requested_addr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid service address %s - %s", svc_requested_addr, err.Error())
		}

		svcaddr = &net.TCPAddr{IP: ap.Addr().AsSlice(), Port: int(ap.Port())}
	}

	if option & RouteOption(ROUTE_OPTION_TCP) != 0 {
		nw = "tcp"
		if svcaddr == nil {
			// port 0 for automatic assignment.
			svcaddr = &net.TCPAddr{Port: 0}
		}
	} else if option & RouteOption(ROUTE_OPTION_TCP4) != 0 {
		nw = "tcp4"
		if svcaddr == nil {
			svcaddr = &net.TCPAddr{IP: net.IPv4zero, Port: 0}
		}
	} else if option & RouteOption(ROUTE_OPTION_TCP6) != 0 {
		nw = "tcp6"
		if svcaddr == nil {
			svcaddr = &net.TCPAddr{IP: net.IPv6zero, Port: 0}
		}
	} else {
		return nil, nil, fmt.Errorf("invalid route option value %d(%s)", option, option.String())
	}

	l, err = net.ListenTCP(nw, svcaddr) // make the binding address configurable. support multiple binding addresses???
	if err != nil { return nil, nil, err }

	svcaddr = l.Addr().(*net.TCPAddr)

	// uniqueness by port id can be checked after listener creation,
	// especially when automatic assignment is requested.
	cts.S.svc_port_mtx.Lock()
	prev_cri, ok = cts.S.svc_port_map[PortId(svcaddr.Port)]
	if ok {
		cts.S.svc_port_mtx.Unlock()
		cts.S.log.Write(cts.Sid, LOG_ERROR,
			"Route(%d,%d) on %s not unique by port number - existing route(%d,%d)",
			cts.Id, id, prev_cri.conn_id, prev_cri.route_id, svcaddr.String())
		l.Close()
		return nil, nil, err
	}
	cts.S.svc_port_map[PortId(svcaddr.Port)] = ConnRouteId{conn_id: cts.Id, route_id: id}
	cts.S.svc_port_mtx.Unlock()

	cts.S.log.Write(cts.Sid, LOG_DEBUG, "Route(%d,%d) listening on %s", cts.Id, id, svcaddr.String())
	return l, svcaddr, nil
}

func (cts *ServerConn) AddNewServerRoute(route_id RouteId, proto RouteOption, ptc_addr string, ptc_name string, svc_requested_addr string, svc_permitted_net string) (*ServerRoute, error) {
	var r *ServerRoute
	var err error

	cts.route_mtx.Lock()
	if cts.route_map[route_id] != nil {
		// If this happens, something must be wrong between the server and the client
		// most likely, it must be a logic error. the state must not go out of sync
		// as the route_id and the peer_id are supposed to be the same between the client
		// and the server.
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf("existent route id - %d", route_id)
	}
	r, err = NewServerRoute(cts, route_id, proto, ptc_addr, ptc_name, svc_requested_addr, svc_permitted_net)
	if err != nil {
		cts.route_mtx.Unlock()
		return nil, err
	}
	cts.route_map[route_id] = r
	cts.S.stats.routes.Add(1)
	cts.route_mtx.Unlock()

	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ServerConn) RemoveServerRoute(route *ServerRoute) error {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route.Id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route.Id)
	}
	if r != route {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route - %d", route.Id)
	}
	delete(cts.route_map, route.Id)
	cts.S.stats.routes.Add(-1)
	cts.route_mtx.Unlock()

	cts.S.svc_port_mtx.Lock()
	delete(cts.S.svc_port_map, PortId(route.SvcAddr.Port))
	cts.S.svc_port_mtx.Unlock()

	r.ReqStop()
	return nil
}

func (cts *ServerConn) RemoveServerRouteById(route_id RouteId) (*ServerRoute, error) {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf("non-existent route id - %d", route_id)
	}
	delete(cts.route_map, route_id)
	cts.S.stats.routes.Add(-1)
	cts.route_mtx.Unlock()

	cts.S.svc_port_mtx.Lock()
	delete(cts.S.svc_port_map, PortId(r.SvcAddr.Port))
	cts.S.svc_port_mtx.Unlock()

	r.ReqStop()
	return r, nil
}

func (cts *ServerConn) FindServerRouteById(route_id RouteId) *ServerRoute {
	var r *ServerRoute
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

func (cts *ServerConn) FindServerPeerConnById(route_id RouteId, peer_id PeerId) *ServerPeerConn {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	defer cts.route_mtx.Unlock()
	r, ok = cts.route_map[route_id]
	if !ok { return nil }

	return r.FindServerPeerConnById(peer_id)
}

func (cts *ServerConn) ReqStopAllServerRoutes() {
	var r *ServerRoute

	cts.route_mtx.Lock()
	for _, r = range cts.route_map { r.ReqStop() }
	cts.route_mtx.Unlock()
}

func (cts *ServerConn) ReportEvent(route_id RouteId, pts_id PeerId, event_type PACKET_KIND, event_data interface{}) error {
	var r *ServerRoute
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

func (cts *ServerConn) receive_from_stream(wg *sync.WaitGroup) {
	var pkt *Packet
	var err error

	defer wg.Done()

	for {
		pkt, err = cts.pss.Recv()
		if errors.Is(err, io.EOF) {
			cts.S.log.Write(cts.Sid, LOG_INFO, "RPC stream closed for client %s", cts.RemoteAddr)
			goto done
		}
		if err != nil {
			cts.S.log.Write(cts.Sid, LOG_ERROR, "RPC stream error for client %s - %s", cts.RemoteAddr, err.Error())
			goto done
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_START:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r *ServerRoute

					r, err = cts.AddNewServerRoute(RouteId(x.Route.RouteId), RouteOption(x.Route.ServiceOption), x.Route.TargetAddrStr, x.Route.TargetName, x.Route.ServiceAddrStr, x.Route.ServiceNetStr)
					if err != nil {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Failed to add route(%d,%s) for %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.RemoteAddr, err.Error())

						err = cts.pss.Send(MakeRouteStoppedPacket(RouteId(x.Route.RouteId), RouteOption(x.Route.ServiceOption), x.Route.TargetAddrStr, x.Route.TargetName, x.Route.ServiceAddrStr, x.Route.ServiceNetStr))
						if err != nil {
							cts.S.log.Write(cts.Sid, LOG_ERROR,
								"Failed to send route_stopped event(%d,%s,%v,%s) to client %s - %s",
								x.Route.RouteId, x.Route.TargetAddrStr,  x.Route.ServiceOption, x.Route.ServiceNetStr, cts.RemoteAddr, err.Error())
							goto done
						} else {
							cts.S.log.Write(cts.Sid, LOG_DEBUG,
								"Sent route_stopped event(%d,%s,%v,%s) to client %s",
								x.Route.RouteId, x.Route.TargetAddrStr,  x.Route.ServiceOption, x.Route.ServiceNetStr, cts.RemoteAddr)
						}

					} else {
						cts.S.log.Write(cts.Sid, LOG_INFO,
							"Added route(%d,%s,%s,%v,%v) for client %s to cts(%d)",
							r.Id, r.PtcAddr, r.SvcAddr.String(), r.SvcOption, r.SvcPermNet, cts.RemoteAddr, cts.Id)
						err = cts.pss.Send(MakeRouteStartedPacket(r.Id, r.SvcOption, r.SvcAddr.String(), r.PtcName, r.SvcReqAddr, r.SvcPermNet.String()))
						if err != nil {
							r.ReqStop()
							cts.S.log.Write(cts.Sid, LOG_ERROR,
								"Failed to send route_started event(%d,%s,%s,%s%v,%v) to client %s - %s",
								r.Id, r.PtcAddr, r.SvcAddr.String(), r.SvcOption, r.SvcPermNet, cts.RemoteAddr, err.Error())
							goto done
						}
					}
				} else {
					cts.S.log.Write(cts.Sid, LOG_INFO, "Received invalid packet from %s", cts.RemoteAddr)
					// TODO: need to abort this client?
				}

			case PACKET_KIND_ROUTE_STOP:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r *ServerRoute

					r, err = cts.RemoveServerRouteById(RouteId(x.Route.RouteId))
					if err != nil {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Failed to delete route(%d,%s) for client %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.RemoteAddr, err.Error())
					} else {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Deleted route(%d,%s,%s,%v,%v) for client %s",
							r.Id, r.PtcAddr, r.SvcAddr.String(), r.SvcOption, r.SvcPermNet.String(), cts.RemoteAddr)
						err = cts.pss.Send(MakeRouteStoppedPacket(r.Id, r.SvcOption, r.PtcAddr, r.PtcName, r.SvcReqAddr, r.SvcPermNet.String()))
						if err != nil {
							r.ReqStop()
							cts.S.log.Write(cts.Sid, LOG_ERROR,
								"Failed to send route_stopped event(%d,%s,%s,%v.%v) to client %s - %s",
								r.Id, r.PtcAddr, r.SvcAddr.String(), r.SvcOption, r.SvcPermNet.String(), cts.RemoteAddr, err.Error())
							goto done
						}
					}
				} else {
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid route_stop event from %s", cts.RemoteAddr)
				}

			case PACKET_KIND_PEER_STARTED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), PACKET_KIND_PEER_STARTED, x.Peer)
					if err != nil {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_started event from %s for peer(%d,%d,%s,%s) - %s",
							cts.RemoteAddr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.S.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_started event from %s for peer(%d,%d,%s,%s)",
							cts.RemoteAddr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					// invalid event data
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_started event from %s", cts.RemoteAddr)
				}

			case PACKET_KIND_PEER_ABORTED:
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), PACKET_KIND_PEER_ABORTED, x.Peer)
					if err != nil {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_aborted event from %s for peer(%d,%d,%s,%s) - %s",
							cts.RemoteAddr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.S.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_aborted event from %s for peer(%d,%d,%s,%s)",
							cts.RemoteAddr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					// invalid event data
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_aborted event from %s", cts.RemoteAddr)
				}

			case PACKET_KIND_PEER_STOPPED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(RouteId(x.Peer.RouteId), PeerId(x.Peer.PeerId), PACKET_KIND_PEER_STOPPED, x.Peer)
					if err != nil {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_stopped event from %s for peer(%d,%d,%s,%s) - %s",
							cts.RemoteAddr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.S.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_stopped event from %s for peer(%d,%d,%s,%s)",
							cts.RemoteAddr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					// invalid event data
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_stopped event from %s", cts.RemoteAddr)
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(RouteId(x.Data.RouteId), PeerId(x.Data.PeerId), PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
						cts.S.log.Write(cts.Sid, LOG_ERROR,
							"Failed to handle peer_data event from %s for peer(%d,%d) - %s",
							cts.RemoteAddr, x.Data.RouteId, x.Data.PeerId, err.Error())
					} else {
						cts.S.log.Write(cts.Sid, LOG_DEBUG,
							"Handled peer_data event from %s for peer(%d,%d)",
							cts.RemoteAddr, x.Data.RouteId, x.Data.PeerId)
					}
				} else {
					// invalid event data
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid peer_data event from %s", cts.RemoteAddr)
				}

			case PACKET_KIND_CONN_DESC:
				var x *Packet_Conn
				var ok bool
				x, ok = pkt.U.(*Packet_Conn)
				if ok {
					if x.Conn.Token == "" {
						cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid conn_desc packet from %s - blank token", cts.RemoteAddr)
						cts.pss.Send(MakeConnErrorPacket(1, "blank token refused"))
						cts.ReqStop() // TODO: is this desirable to disconnect?
					} else if x.Conn.Token != cts.ClientToken {
						_, err = strconv.ParseUint(x.Conn.Token, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
						if err == nil { // this is not != nil. this is to check if the token is numeric
							cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid conn_desc packet from %s - numeric token '%s'", cts.RemoteAddr, x.Conn.Token)
							cts.pss.Send(MakeConnErrorPacket(1, "numeric token refused"))
							cts.ReqStop() // TODO: is this desirable to disconnect?
						} else {
							cts.S.cts_mtx.Lock()
							_, ok = cts.S.cts_map_by_token[x.Conn.Token]
							if ok {
								// error
								cts.S.cts_mtx.Unlock()
								cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid conn_desc packet from %s - duplicate token '%s'", cts.RemoteAddr, x.Conn.Token)
								cts.pss.Send(MakeConnErrorPacket(1, fmt.Sprintf("duplicate token refused - %s", x.Conn.Token)))
								cts.ReqStop() // TODO: is this desirable to disconnect?
							} else {
								if cts.ClientToken != "" { delete(cts.S.cts_map_by_token, cts.ClientToken) }
								cts.ClientToken = x.Conn.Token
								cts.S.cts_map_by_token[x.Conn.Token] = cts
								cts.S.cts_mtx.Unlock()
								cts.S.log.Write(cts.Sid, LOG_INFO, "client(%d) %s - token set to '%s'", cts.Id, cts.RemoteAddr, x.Conn.Token)
							}
						}
					}
				} else {
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid conn_desc packet from %s", cts.RemoteAddr)
				}

			case PACKET_KIND_CONN_NOTICE:
				// the connection from the client to a peer has been established
				var x *Packet_ConnNoti
				var ok bool
				x, ok = pkt.U.(*Packet_ConnNoti)
				if ok {
					if cts.S.conn_notice != nil {
						cts.S.conn_notice.Handle(cts, x.ConnNoti.Text)
					}
				} else {
					cts.S.log.Write(cts.Sid, LOG_ERROR, "Invalid conn_notice packet from %s", cts.RemoteAddr)
				}
		}
	}

done:
	cts.S.log.Write(cts.Sid, LOG_INFO, "RPC stream receiver ended")
}

func (cts *ServerConn) RunTask(wg *sync.WaitGroup) {
	var strm *GuardedPacketStreamServer
	var ctx context.Context

	defer wg.Done()

	strm = cts.pss
	ctx = strm.Context()

	// it looks like the only proper way to interrupt the blocking Recv
	// call on the grpc streaming server is exit from the service handler
	// which is this function invoked from PacketStream().
	// there is no cancel function or whatever that can interrupt it.
	// so start the Recv() loop in a separte goroutine and let this
	// function be the channel waiter only.
	// increment on the wait group is for the caller to wait for
	// these detached goroutines to finish.
	wg.Add(1)
	go cts.receive_from_stream(wg)

	for {
		// exit if context is done
		// or continue
		select {
			case <-ctx.Done(): // the stream context is done
				cts.S.log.Write(cts.Sid, LOG_INFO, "RPC stream done - %s", ctx.Err().Error())
				goto done

			case <- cts.stop_chan:
				// get out of the loop to eventually to exit from
				// this handler to let the main grpc server to
				// close this specific client connection.
				goto done

			//default:
				// no other case is ready.
				// without the default case, the select construct would block
		}
	}

done:
	cts.ReqStop() // just in case
	cts.route_wg.Wait()
	cts.S.log.Write(cts.Sid, LOG_INFO, "End of connection task")
}

func (cts *ServerConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		var r *ServerRoute

		cts.route_mtx.Lock()
		for _, r = range cts.route_map { r.ReqStop() }
		cts.route_mtx.Unlock()

		// there is no good way to break a specific connection client to
		// the grpc server. while the global grpc server is closed in
		// ReqStop() for Server, the individuation connection is closed
		// by returing from the grpc handler goroutine. See the comment
		// RunTask() for ServerConn.
		cts.stop_chan <- true
	}
}

// --------------------------------------------------------------------

func (s *Server) GetSeed(ctx context.Context, c_seed *Seed) (*Seed, error) {
	var s_seed Seed

	// seed exchange is for furture expansion of the protocol
	// there is nothing to do much about it for now.

	s_seed.Version = HODU_RPC_VERSION
	s_seed.Flags = 0

	// we create no ServerConn structure associated with the connection
	// at this phase for the server. it doesn't track the client version and
	// features. we delegate protocol selection solely to the client.

	return &s_seed, nil
}

func (s *Server) PacketStream(strm Hodu_PacketStreamServer) error {
	var ctx context.Context
	var p *peer.Peer
	var ok bool
	var err error
	var cts *ServerConn

	ctx = strm.Context()
	p, ok = peer.FromContext(ctx)
	if !ok {
		return fmt.Errorf("failed to get peer from packet stream context")
	}

	cts, err = s.AddNewServerConn(&p.Addr, &p.LocalAddr, strm)
	if err != nil {
		return fmt.Errorf("unable to add client %s - %s", p.Addr.String(), err.Error())
	}

	// Don't detached the cts task as a go-routine as this function
	// is invoked as a go-routine by the grpc server.
	s.cts_wg.Add(1)
	cts.RunTask(&s.cts_wg)
	return nil
}

// ------------------------------------

type ConnCatcher struct {
	server *Server
}

func (cc *ConnCatcher) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return ctx
}

func (cc *ConnCatcher) HandleRPC(ctx context.Context, s stats.RPCStats) {
}

func (cc *ConnCatcher) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return ctx
	//return context.TODO()
}

func (cc *ConnCatcher) HandleConn(ctx context.Context, cs stats.ConnStats) {
	var p *peer.Peer
	var ok bool
	var addr string

	p, ok = peer.FromContext(ctx)
	if !ok {
		addr = ""
	} else {
		addr = p.Addr.String()
	}

	/*
	md,ok:=metadata.FromIncomingContext(ctx)
	if ok {
	}*/

	switch cs.(type) {
		case *stats.ConnBegin:
			cc.server.log.Write("", LOG_INFO, "Client connected - %s", addr)

		case *stats.ConnEnd:
			var cts *ServerConn
			var log_id string
			cts, _ = cc.server.RemoveServerConnByAddr(p.Addr)
			if cts != nil { log_id = cts.Sid }
			cc.server.log.Write(log_id, LOG_INFO, "Client disconnected - %s", addr)
	}
}

// ------------------------------------
func (m ServerPeerConnMap) get_sorted_keys() []PeerId {
	var ks []PeerId
	var peer *ServerPeerConn

	ks = make([]PeerId, 0, len(m))
	for _, peer = range m {
		ks = append(ks, peer.conn_id)
	}
	slices.Sort(ks)
	return ks
}

func (m ServerRouteMap) get_sorted_keys() []RouteId {
	var ks []RouteId
	var route *ServerRoute

	ks = make([]RouteId, 0, len(m))
	for _, route = range m {
		ks = append(ks, route.Id)
	}
	slices.Sort(ks)
	return ks
}

func (m ServerConnMap) get_sorted_keys() []ConnId {
	var ks []ConnId
	var cts *ServerConn

	ks = make([]ConnId, 0, len(m))
	for _, cts = range m {
		ks = append(ks, cts.Id)
	}
	slices.Sort(ks)
	return ks
}
// ------------------------------------

type wrappedStream struct {
	grpc.ServerStream
}

func (w *wrappedStream) RecvMsg(msg interface{}) error {
	return w.ServerStream.RecvMsg(msg)
}

func (w *wrappedStream) SendMsg(msg interface{}) error {
	return w.ServerStream.SendMsg(msg)
}

func newWrappedStream(s grpc.ServerStream) grpc.ServerStream {
	return &wrappedStream{s}
}

func streamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	var err error

	// authentication (token verification)
/*
	md, ok = metadata.FromIncomingContext(ss.Context())
	if !ok {
		return errMissingMetadata
	}
	if !valid(md["authorization"]) {
		return errInvalidToken
	}
*/

	err = handler(srv, newWrappedStream(ss))
	if err != nil {
		// TODO: LOGGING
	}
	return err
}

func unaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	var v interface{}
	var err error

	// authentication (token verification)
/*
	md, ok = metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, errMissingMetadata
	}
	if !valid(md["authorization"]) {
//		return nil, errInvalidToken
	}
*/

	v, err = handler(ctx, req)
	if err != nil {
		//fmt.Printf("RPC failed with error: %v\n", err)
		// TODO: Logging?
	}

	return v, err
}


type server_http_log_writer struct {
	svr *Server
}

func (hlw *server_http_log_writer) Write(p []byte) (n int, err error) {
	// the standard http.Server always requires *log.Logger
	// use this iowriter to create a logger to pass it to the http server.
	// since this is another log write wrapper, give adjustment value
	hlw.svr.log.WriteWithCallDepth("", LOG_INFO, +1, string(p))
	return len(p), nil
}

type ServerHttpHandler interface {
	Id() string
	Cors(req *http.Request) bool
	Authenticate(req *http.Request) (int, string)
	ServeHTTP (w http.ResponseWriter, req *http.Request) (int, error)
}

func (s *Server) WrapHttpHandler(handler ServerHttpHandler) http.Handler {
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
			if err != nil { dump_call_frame_and_exit(s.log, req, err) }
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
		time_taken = time.Now().Sub(start_time)

		if status_code > 0 {
			if err != nil {
				s.log.Write(handler.Id(), LOG_INFO, "[%s] %s %s %d %.9f - %s", req.RemoteAddr, req.Method, req.URL.String(), status_code, time_taken.Seconds(), err.Error())
			} else {
				s.log.Write(handler.Id(), LOG_INFO, "[%s] %s %s %d %.9f", req.RemoteAddr, req.Method, req.URL.String(), status_code, time_taken.Seconds())
			}
		}
	})
}

func NewServer(ctx context.Context, name string, logger Logger, cfg *ServerConfig) (*Server, error) {
	var s Server
	var l *net.TCPListener
	var rpcaddr *net.TCPAddr
	var addr string
	var gl *net.TCPListener
	var i int
	var hs_log *log.Logger
	var opts []grpc.ServerOption
	var err error

	if len(cfg.RpcAddrs) <= 0 {
		return nil, fmt.Errorf("no server addresses provided")
	}

	s.Ctx, s.CtxCancel = context.WithCancel(ctx)
	s.name = name
	s.log = logger
	/* create the specified number of listeners */
	s.rpc = make([]*net.TCPListener, 0)
	for _, addr = range cfg.RpcAddrs {
		var addr_class string

		addr_class = TcpAddrStrClass(addr)
		rpcaddr, err = net.ResolveTCPAddr(addr_class, addr) // Make this interruptable???
		if err != nil { goto oops }

		l, err = net.ListenTCP(addr_class, rpcaddr)
		if err != nil { goto oops }

		s.rpc = append(s.rpc, l)
	}

	s.Cfg = cfg
	s.ext_svcs = make([]Service, 0, 1)
	s.pts_limit = cfg.MaxPeers
	s.cts_limit = cfg.RpcMaxConns
	s.cts_next_id = 1
	s.cts_map = make(ServerConnMap)
	s.cts_map_by_addr = make(ServerConnMapByAddr)
	s.cts_map_by_token = make(ServerConnMapByClientToken)
	s.svc_port_map = make(ServerSvcPortMap)
	s.stop_chan = make(chan bool, 8)
	s.stop_req.Store(false)

/*
	creds, err := credentials.NewServerTLSFromFile(data.Path("x509/server_cert.pem"), data.Path("x509/server_key.pem"))
	if err != nil {
	  log.Fatalf("failed to create credentials: %v", err)
	}
	gs = grpc.NewServer(grpc.Creds(creds))
*/

	opts = append(opts, grpc.StatsHandler(&ConnCatcher{server: &s}))
	if s.Cfg.RpcTls != nil { opts = append(opts, grpc.Creds(credentials.NewTLS(s.Cfg.RpcTls))) }
	//opts = append(opts, grpc.UnaryInterceptor(unaryInterceptor))
	//opts = append(opts, grpc.StreamInterceptor(streamInterceptor))
	s.rpc_svr = grpc.NewServer(opts...)
	RegisterHoduServer(s.rpc_svr, &s)

	// ---------------------------------------------------------

	hs_log = log.New(&server_http_log_writer{svr: &s}, "", 0)

	// ---------------------------------------------------------

	s.ctl_mux = http.NewServeMux()

	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/server-conns",
		s.WrapHttpHandler(&server_ctl_server_conns{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/server-conns/{conn_id}",
		s.WrapHttpHandler(&server_ctl_server_conns_id{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/server-conns/{conn_id}/routes",
		s.WrapHttpHandler(&server_ctl_server_conns_id_routes{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/server-conns/{conn_id}/routes/{route_id}",
		s.WrapHttpHandler(&server_ctl_server_conns_id_routes_id{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/server-conns/{conn_id}/routes/{route_id}/peers",
		s.WrapHttpHandler(&server_ctl_server_conns_id_routes_id_peers{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/server-conns/{conn_id}/routes/{route_id}/peers/{peer_id}",
		s.WrapHttpHandler(&server_ctl_server_conns_id_routes_id_peers_id{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/notices",
		s.WrapHttpHandler(&server_ctl_notices{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/notices/{conn_id}",
		s.WrapHttpHandler(&server_ctl_notices_id{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/stats",
		s.WrapHttpHandler(&server_ctl_stats{server_ctl{s: &s, id: HS_ID_CTL}}))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/token",
		s.WrapHttpHandler(&server_ctl_token{server_ctl{s: &s, id: HS_ID_CTL}}))

// TODO: make this optional. add this endpoint only if it's enabled...
	s.promreg = prometheus.NewRegistry()
	s.promreg.MustRegister(prometheus.NewGoCollector())
	s.promreg.MustRegister(NewServerCollector(&s))
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl/metrics",
		promhttp.HandlerFor(s.promreg, promhttp.HandlerOpts{ EnableOpenMetrics: true }))

	s.ctl = make([]*http.Server, len(cfg.CtlAddrs))
	for i = 0; i < len(cfg.CtlAddrs); i++ {
		s.ctl[i] = &http.Server{
			Addr: cfg.CtlAddrs[i],
			Handler: s.ctl_mux,
			TLSConfig: s.Cfg.CtlTls,
			ErrorLog: hs_log,
			// TODO: more settings
		}
	}

	// ---------------------------------------------------------

	s.pxy_ws = &server_proxy_ssh_ws{s: &s, id: HS_ID_PXY_WS}
	s.pxy_mux = http.NewServeMux() // TODO: make /_init,_ssh,_ssh_ws,_http configurable...
	s.pxy_mux.Handle("/_ssh-ws/{conn_id}/{route_id}",
		websocket.Handler(func(ws *websocket.Conn) { s.pxy_ws.ServeWebsocket(ws) }))
	s.pxy_mux.Handle("/_ssh/server-conns/{conn_id}/routes/{route_id}",
		s.WrapHttpHandler(&server_ctl_server_conns_id_routes_id{server_ctl{s: &s, id: HS_ID_PXY, noauth: true}}))
	s.pxy_mux.Handle("/_ssh/{conn_id}/",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "_redirect"}))
	s.pxy_mux.Handle("/_ssh/{conn_id}/{route_id}/",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "xterm.html"}))
	s.pxy_mux.Handle("/_ssh/xterm.js",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "xterm.js"}))
	s.pxy_mux.Handle("/_ssh/xterm-addon-fit.js",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "xterm-addon-fit.js"}))
	s.pxy_mux.Handle("/_ssh/xterm.css",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "xterm.css"}))
	s.pxy_mux.Handle("/_ssh/",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "_forbidden"}))
	s.pxy_mux.Handle("/favicon.ico",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "_forbidden"}))
	s.pxy_mux.Handle("/favicon.ico/",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, file: "_forbidden"}))

	s.pxy_mux.Handle("/_http/{conn_id}/{route_id}/{trailer...}",
		s.WrapHttpHandler(&server_proxy_http_main{server_proxy: server_proxy{s: &s, id: HS_ID_PXY}, prefix: "/_http"}))

	s.pxy = make([]*http.Server, len(cfg.PxyAddrs))

	for i = 0; i < len(cfg.PxyAddrs); i++ {
		s.pxy[i] = &http.Server{
			Addr: cfg.PxyAddrs[i],
			Handler: s.pxy_mux,
			TLSConfig: cfg.PxyTls,
			ErrorLog: hs_log,
			// TODO: more settings
		}
	}

	// ---------------------------------------------------------

	s.wpx_mux = http.NewServeMux()

	s.wpx_ws = &server_proxy_ssh_ws{s: &s, id: "wpx-ssh"}
	s.wpx_mux = http.NewServeMux() // TODO: make /_init,_ssh,_ssh_ws,_http configurable...
	s.wpx_mux.Handle("/_ssh-ws/{conn_id}/{route_id}",
		websocket.Handler(func(ws *websocket.Conn) { s.wpx_ws.ServeWebsocket(ws) }))

	s.wpx_mux.Handle("/_ssh/server-conns/{conn_id}/routes/{route_id}",
		s.WrapHttpHandler(&server_ctl_server_conns_id_routes_id{server_ctl{s: &s, id: HS_ID_WPX, noauth: true}}))
	s.wpx_mux.Handle("/_ssh/xterm.js",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}, file: "xterm.js"}))
	s.wpx_mux.Handle("/_ssh/xterm-addon-fit.js",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}, file: "xterm-addon-fit.js"}))
	s.wpx_mux.Handle("/_ssh/xterm.css",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}, file: "xterm.css"}))
	s.wpx_mux.Handle("/_ssh/{port_id}",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}, file: "xterm.html"}))
	s.wpx_mux.Handle("/_ssh/",
		s.WrapHttpHandler(&server_proxy_xterm_file{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}, file: "_forbidden"}))

	s.wpx_mux.Handle("/{port_id}/{trailer...}",
		s.WrapHttpHandler(&server_proxy_http_main{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}, prefix: PORT_ID_MARKER}))
	s.wpx_mux.Handle("/",
		s.WrapHttpHandler(&server_proxy_http_wpx{server_proxy: server_proxy{s: &s, id: HS_ID_WPX}}))

	s.wpx = make([]*http.Server, len(cfg.WpxAddrs))

	for i = 0; i < len(cfg.WpxAddrs); i++ {
		s.wpx[i] = &http.Server{
			Addr: cfg.WpxAddrs[i],
			Handler: s.wpx_mux,
			TLSConfig: cfg.WpxTls,
			ErrorLog: hs_log,
		}
	}
	// ---------------------------------------------------------

	s.stats.conns.Store(0)
	s.stats.routes.Store(0)
	s.stats.peers.Store(0)
	s.stats.ssh_proxy_sessions.Store(0)

	return &s, nil

oops:
	if gl != nil { gl.Close() }
	for _, l = range s.rpc { l.Close() }
	s.rpc = make([]*net.TCPListener, 0)
	return nil, err
}

func (s *Server) SetWpxResponseTransformer(tf ServerWpxResponseTransformer) {
	s.wpx_resp_tf = tf
}

func (s *Server) GetWpxResponseTransformer() ServerWpxResponseTransformer {
	return s.wpx_resp_tf
}

func (s *Server) SetWpxForeignPortProxyMaker(pm ServerWpxForeignPortProxyMaker) {
	s.wpx_foreign_port_proxy_maker = pm
}

func (s *Server) GetWpxForeignPortProxyMaker() ServerWpxForeignPortProxyMaker {
	return s.wpx_foreign_port_proxy_maker
}


func (s *Server) SetXtermHtml(html string) {
	s.xterm_html = html
}

func (s *Server) GetXtermHtml() string {
	return s.xterm_html
}

func (s *Server) run_grpc_server(idx int, wg *sync.WaitGroup) error {
	var l *net.TCPListener
	var err error

	defer wg.Done()

	l = s.rpc[idx]
	// it seems to be safe to call a single grpc server on differnt listening sockets multiple times
	s.log.Write("", LOG_INFO, "Starting RPC server on %s", l.Addr().String())
	err = s.rpc_svr.Serve(l)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			s.log.Write("", LOG_INFO, "RPC server on %s closed", l.Addr().String())
		} else {
			s.log.Write("", LOG_ERROR, "Error from RPC server on %s - %s", l.Addr().String(), err.Error())
		}
		return err
	}

	return nil
}

func (s *Server) RunTask(wg *sync.WaitGroup) {
	var idx int

	defer wg.Done()

	for idx, _ = range s.rpc {
		s.rpc_wg.Add(1)
		go s.run_grpc_server(idx, &s.rpc_wg)
	}

	// most the work is done by in separate goroutines (s.run_grp_server)
	// this loop serves as a placeholder to prevent the logic flow from
	// descening down to s.ReqStop()
task_loop:
	for {
		select {
			case <-s.stop_chan:
				break task_loop
		}
	}

	s.ReqStop()

	s.rpc_wg.Wait()
	s.log.Write("", LOG_DEBUG, "All RPC listeners ended")

	s.cts_wg.Wait()
	s.log.Write("", LOG_DEBUG, "All CTS handlers ended")

	// stop the main grpc server after all the other tasks are finished.
	s.rpc_svr.Stop()
}

func (s *Server) RunCtlTask(wg *sync.WaitGroup) {
	var err error
	var ctl *http.Server
	var idx int
	var l_wg sync.WaitGroup

	defer wg.Done()

	for idx, ctl = range s.ctl {
		l_wg.Add(1)
		go func(i int, cs *http.Server) {
			var l net.Listener

			s.log.Write("", LOG_INFO, "Control channel[%d] started on %s", i, s.Cfg.CtlAddrs[i])

			if s.stop_req.Load() == false {
				// defeat hard-coded "tcp" in ListenAndServe() and ListenAndServeTLS()
				//  err = cs.ListenAndServe()
				//  err = cs.ListenAndServeTLS("", "")
				l, err = net.Listen(TcpAddrStrClass(cs.Addr), cs.Addr)
				if err == nil {
					if s.stop_req.Load() == false {
						if s.Cfg.CtlTls == nil {
							err = cs.Serve(l)
						} else {
							err = cs.ServeTLS(l, "", "") // s.Cfg.CtlTls must provide a certificate and a key
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
				s.log.Write("", LOG_INFO, "Control channel[%d] ended", i)
			} else {
				s.log.Write("", LOG_ERROR, "Control channel[%d] error - %s", i, err.Error())
			}
			l_wg.Done()
		}(idx, ctl)
	}
	l_wg.Wait()
}

func (s *Server) RunPxyTask(wg *sync.WaitGroup) {
	var err error
	var pxy *http.Server
	var idx int
	var l_wg sync.WaitGroup

	defer wg.Done()

	for idx, pxy = range s.pxy {
		l_wg.Add(1)
		go func(i int, cs *http.Server) {
			var l net.Listener

			s.log.Write("", LOG_INFO, "Proxy channel[%d] started on %s", i, s.Cfg.PxyAddrs[i])

			if s.stop_req.Load() == false {
				l, err = net.Listen(TcpAddrStrClass(cs.Addr), cs.Addr)
				if err == nil {
					if s.stop_req.Load() == false {
						if s.Cfg.PxyTls == nil { // TODO: change this
							err = cs.Serve(l)
						} else {
							err = cs.ServeTLS(l, "", "") // s.Cfg.PxyTls must provide a certificate and a key
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
				s.log.Write("", LOG_INFO, "Proxy channel[%d] ended", i)
			} else {
				s.log.Write("", LOG_ERROR, "Proxy channel[%d] error - %s", i, err.Error())
			}
			l_wg.Done()
		}(idx, pxy)
	}
	l_wg.Wait()
}

func (s *Server) RunWpxTask(wg *sync.WaitGroup) {
	var err error
	var wpx *http.Server
	var idx int
	var l_wg sync.WaitGroup

	defer wg.Done()

	for idx, wpx = range s.wpx {
		l_wg.Add(1)
		go func(i int, cs *http.Server) {
			var l net.Listener

			s.log.Write("", LOG_INFO, "Wpx channel[%d] started on %s", i, s.Cfg.WpxAddrs[i])

			if s.stop_req.Load() == false {
				l, err = net.Listen(TcpAddrStrClass(cs.Addr), cs.Addr)
				if err == nil {
					if s.stop_req.Load() == false {
						if s.Cfg.WpxTls == nil { // TODO: change this
							err = cs.Serve(l)
						} else {
							err = cs.ServeTLS(l, "", "") // s.Cfg.WpxTls must provide a certificate and a key
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
				s.log.Write("", LOG_INFO, "Wpx channel[%d] ended", i)
			} else {
				s.log.Write("", LOG_ERROR, "Wpx channel[%d] error - %s", i, err.Error())
			}
			l_wg.Done()
		}(idx, wpx)
	}
	l_wg.Wait()
}

func (s *Server) ReqStop() {
	if s.stop_req.CompareAndSwap(false, true) {
		var l *net.TCPListener
		var cts *ServerConn
		var hs *http.Server

		// call cancellation function before anything else
		// to break sub-tasks relying on this server context.
		// for example, http.Client in server_proxy_http_main
		s.CtxCancel()

		for _, hs = range s.ctl {
			hs.Shutdown(s.Ctx) // to break s.ctl.Serve()
		}

		for _, hs = range s.pxy {
			hs.Shutdown(s.Ctx) // to break s.pxy.Serve()
		}

		for _, hs = range s.wpx {
			hs.Shutdown(s.Ctx) // to break s.wpx.Serve()
		}

		//s.rpc_svr.GracefulStop()
		//s.rpc_svr.Stop()
		for _, l = range s.rpc {
			l.Close()
		}

		// request to stop connections from/to peer held in the cts structure
		s.cts_mtx.Lock()
		for _, cts = range s.cts_map { cts.ReqStop() }
		s.cts_mtx.Unlock()

		s.stop_chan <- true
	}
}

func (s *Server) AddNewServerConn(remote_addr *net.Addr, local_addr *net.Addr, pss Hodu_PacketStreamServer) (*ServerConn, error) {
	var cts ServerConn
	var start_id ConnId
	var assigned_id ConnId
	var ok bool

	cts.S = s
	cts.route_map = make(ServerRouteMap)
	cts.RemoteAddr = *remote_addr
	cts.LocalAddr = *local_addr
	cts.pss = &GuardedPacketStreamServer{Hodu_PacketStreamServer: pss}

	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 8)

	s.cts_mtx.Lock()

	if s.cts_limit > 0 && len(s.cts_map) >= s.cts_limit {
		s.cts_mtx.Unlock()
		return nil, fmt.Errorf("too many connections - %d", s.cts_limit)
	}

	//start_id = rand.Uint64()
	//start_id = ConnId(monotonic_time() / 1000)
	start_id = s.cts_next_id
	for {
		_, ok = s.cts_map[s.cts_next_id]
		if !ok {
			assigned_id = s.cts_next_id
			s.cts_next_id++
			if s.cts_next_id == 0 { s.cts_next_id++ }
			break
		}
		s.cts_next_id++
		if s.cts_next_id == 0 { s.cts_next_id++ }
		if s.cts_next_id == start_id {
			s.cts_mtx.Unlock()
			return nil, fmt.Errorf("unable to assign id")
		}
	}
	cts.Id = assigned_id
	cts.Sid = fmt.Sprintf("%d", cts.Id) // id in string used for logging

	_, ok = s.cts_map_by_addr[cts.RemoteAddr]
	if ok {
		s.cts_mtx.Unlock()
		return nil, fmt.Errorf("existing client address - %s", cts.RemoteAddr.String())
	}
	if cts.ClientToken != "" {
		// this check is not needed as Token is never set at this phase
		// however leave statements here for completeness
		_, ok = s.cts_map_by_token[cts.ClientToken]
		if ok {
			s.cts_mtx.Unlock()
			return nil, fmt.Errorf("existing client token - %s", cts.ClientToken)
		}
		s.cts_map_by_token[cts.ClientToken] = &cts
	}
	s.cts_map_by_addr[cts.RemoteAddr] = &cts
	s.cts_map[cts.Id] = &cts
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.cts_mtx.Unlock()

	s.log.Write(cts.Sid, LOG_DEBUG, "Added client connection from %s", cts.RemoteAddr.String())
	return &cts, nil
}

func (s *Server) ReqStopAllServerConns() {
	var cts *ServerConn
	s.cts_mtx.Lock()
	for _, cts = range s.cts_map { cts.ReqStop() }
	s.cts_mtx.Unlock()
}

func (s *Server) RemoveServerConn(cts *ServerConn) error {
	var conn *ServerConn
	var ok bool

	s.cts_mtx.Lock()

	conn, ok = s.cts_map[cts.Id]
	if !ok {
		s.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.Id)
	}
	if conn != cts {
		s.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.Id)
	}

	delete(s.cts_map, cts.Id)
	delete(s.cts_map_by_addr, cts.RemoteAddr)
	if cts.ClientToken != "" { delete(s.cts_map_by_token, cts.ClientToken) }
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.cts_mtx.Unlock()

	cts.ReqStop()
	return nil
}

func (s *Server) RemoveServerConnByAddr(addr net.Addr) (*ServerConn, error) {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()

	cts, ok = s.cts_map_by_addr[addr]
	if !ok {
		s.cts_mtx.Unlock()
		return nil, fmt.Errorf("non-existent connection address - %s", addr.String())
	}
	delete(s.cts_map, cts.Id)
	delete(s.cts_map_by_addr, cts.RemoteAddr)
	if cts.ClientToken != "" { delete(s.cts_map_by_token, cts.ClientToken) }
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.cts_mtx.Unlock()

	cts.ReqStop()
	return cts, nil
}

func (s *Server) RemoveServerConnByClientToken(token string) (*ServerConn, error) {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()

	cts, ok = s.cts_map_by_token[token]
	if !ok {
		s.cts_mtx.Unlock()
		return nil, fmt.Errorf("non-existent connection token - %s", token)
	}
	delete(s.cts_map, cts.Id)
	delete(s.cts_map_by_addr, cts.RemoteAddr)
	delete(s.cts_map_by_token, cts.ClientToken) // no Empty check becuase an empty token is never found in the map
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.cts_mtx.Unlock()

	cts.ReqStop()
	return cts, nil
}

func (s *Server) FindServerConnById(id ConnId) *ServerConn {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map[id]
	if !ok { return nil }

	return cts
}

func (s *Server) FindServerConnByAddr(addr net.Addr) *ServerConn {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map_by_addr[addr]
	if !ok { return nil }

	return cts
}

func (s *Server) FindServerConnByClientToken(token string) *ServerConn {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map_by_token[token]
	if !ok { return nil }

	return cts
}

func (s *Server) FindServerRouteById(id ConnId, route_id RouteId) *ServerRoute {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map[id]
	if !ok { return nil }

	return cts.FindServerRouteById(route_id)
}

func (s *Server) FindServerPeerConnById(id ConnId, route_id RouteId, peer_id PeerId) *ServerPeerConn {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map[id]
	if !ok { return nil }

	return cts.FindServerPeerConnById(route_id, peer_id)
}

func (s *Server) FindServerRouteByPortId(port_id PortId) *ServerRoute {
	var cri ConnRouteId
	var ok bool

	s.svc_port_mtx.Lock()
	defer s.svc_port_mtx.Unlock()

	cri, ok = s.svc_port_map[port_id]
	if !ok { return nil }
	return s.FindServerRouteById(cri.conn_id, cri.route_id)
}

func (s *Server) FindServerPeerConnByPortId(port_id PortId, peer_id PeerId) *ServerPeerConn {
	var cri ConnRouteId
	var ok bool

	s.svc_port_mtx.Lock()
	defer s.svc_port_mtx.Unlock()

	cri, ok = s.svc_port_map[port_id]
	if !ok { return nil }
	return s.FindServerPeerConnById(cri.conn_id, cri.route_id, peer_id)
}

func (s *Server) FindServerPeerConnByIdStr(conn_id string, route_id string, peer_id string) (*ServerPeerConn, error) {
	var p *ServerPeerConn
	var err error

	if route_id == PORT_ID_MARKER {
		var port_nid uint64
		var peer_nid uint64

		port_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid port id %s - %s", conn_id, err.Error()) }

		peer_nid, err = strconv.ParseUint(peer_id, 10, int(unsafe.Sizeof(PeerId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid peer id %s - %s", peer_id, err.Error()) }

		p = s.FindServerPeerConnByPortId(PortId(port_nid), PeerId(peer_nid))
		if p == nil { return nil, fmt.Errorf("peer(%d,%d) not found", port_nid, peer_nid) }
	} else {
		var conn_nid uint64
		var route_nid uint64
		var peer_nid uint64

		conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error()) }

		route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid route id %s - %s", route_id, err.Error()) }

		peer_nid, err = strconv.ParseUint(peer_id, 10, int(unsafe.Sizeof(PeerId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid peer id %s - %s", peer_id, err.Error()) }

		p = s.FindServerPeerConnById(ConnId(conn_nid), RouteId(route_nid), PeerId(peer_nid))
		if p == nil { return nil, fmt.Errorf("peer(%d,%d,%d) not found", conn_nid, route_nid, peer_nid) }
	}

	return p, nil
}

func (s *Server) FindServerRouteByIdStr(conn_id string, route_id string) (*ServerRoute, error) {
	var r *ServerRoute
	var err error

	if route_id == PORT_ID_MARKER {
		var port_nid uint64

		port_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(PortId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid port id %s - %s", conn_id, err.Error()) }

		r = s.FindServerRouteByPortId(PortId(port_nid))
		if r == nil { return nil, fmt.Errorf("port(%d) not found", port_nid) }
	} else {
		var conn_nid uint64
		var route_nid uint64

		conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error()) }

		route_nid, err = strconv.ParseUint(route_id, 10, int(unsafe.Sizeof(RouteId(0)) * 8))
		if err != nil { return nil, fmt.Errorf("invalid route id %s - %s", route_id, err.Error()) }

		r = s.FindServerRouteById(ConnId(conn_nid), RouteId(route_nid))
		if r == nil { return nil, fmt.Errorf("route(%d,%d) not found", conn_nid, route_nid) }
	}

	return r, nil
}

func (s *Server) FindServerConnByIdStr(conn_id string) (*ServerConn, error) {
	var conn_nid uint64
	var cts *ServerConn
	var err error

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err != nil {
		//return nil, fmt.Errorf("invalid connection id %s - %s", conn_id, err.Error());
		cts = s.FindServerConnByClientToken(conn_id) // if not numeric, attempt to use it as a token
		if cts == nil { return nil, fmt.Errorf("non-existent connection token '%s'", conn_id) }
	} else {
		cts = s.FindServerConnById(ConnId(conn_nid))
		if cts == nil { return nil, fmt.Errorf("non-existent connection id %d", conn_nid) }
	}

	return cts, nil
}

func (s *Server) StartService(cfg interface{}) {
	s.wg.Add(1)
	go s.RunTask(&s.wg)
}

func (s *Server) StartExtService(svc Service, data interface{}) {
	s.ext_mtx.Lock()
	s.ext_svcs = append(s.ext_svcs, svc)
	s.ext_mtx.Unlock()
	s.wg.Add(1)
	go svc.RunTask(&s.wg)
}

func (s *Server) StartCtlService() {
	s.wg.Add(1)
	go s.RunCtlTask(&s.wg)
}

func (s *Server) StartPxyService() {
	s.wg.Add(1)
	go s.RunPxyTask(&s.wg)
}

func (s *Server) StartWpxService() {
	s.wg.Add(1)
	go s.RunWpxTask(&s.wg)
}

func (s *Server) StopServices() {
	var ext_svc Service
	s.ReqStop()
	for _, ext_svc = range s.ext_svcs {
		ext_svc.StopServices()
	}
}

func (s *Server) FixServices() {
	s.log.Rotate()
}

func (s *Server) WaitForTermination() {
	s.wg.Wait()
}

func (s *Server) WriteLog(id string, level LogLevel, fmtstr string, args ...interface{}) {
	s.log.Write(id, level, fmtstr, args...)
}

func (s *Server) SetConnNoticeHandler(handler ServerConnNoticeHandler) {
	s.conn_notice = handler
}

func (s *Server) AddCtlHandler(path string, handler ServerHttpHandler) {
	s.ctl_mux.Handle(s.Cfg.CtlPrefix + "/_ctl" + path, s.WrapHttpHandler(handler))
}

func (s *Server) AddCtlMetricsCollector(col prometheus.Collector) error {
	return s.promreg.Register(col)
}

func (s *Server) RemoveCtlMetricsCollector(col prometheus.Collector) bool {
	return s.promreg.Unregister(col)
}

func (s *Server) SendNotice(id_str string, text string) error {
	var cts *ServerConn
	var err error

	if id_str != "" {
		cts, err = s.FindServerConnByIdStr(id_str) // this function accepts connection id as well as the token.
		if err != nil { return err }
		err = cts.pss.Send(MakeConnNoticePacket(text))
		if err != nil {
			return fmt.Errorf("failed to send conn_notice text '%s' to %s - %s", text, cts.RemoteAddr, err.Error())
		}
	} else {
		s.cts_mtx.Lock()
		// TODO: what if this loop takes too long? in that case,
		//       lock is held for long. think about how to handle this.
		for _, cts = range s.cts_map {
			cts.pss.Send(MakeConnNoticePacket(text))
			// let's not care about an error when broacasting a notice to all connections
		}
		s.cts_mtx.Unlock()
	}

	return nil
}
