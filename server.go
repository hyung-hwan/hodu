package hodu

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "io"
import "log"
import "math/rand"
import "net"
import "net/http"
import "net/netip"
import "os"
import "sync"
import "sync/atomic"

import "google.golang.org/grpc"
import "google.golang.org/grpc/credentials"
//import "google.golang.org/grpc/metadata"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/stats"

const PTS_LIMIT int = 16384
const CTS_LIMIT int = 16384

type ServerConnMapByAddr = map[net.Addr]*ServerConn
type ServerConnMap = map[uint32]*ServerConn
type ServerPeerConnMap = map[uint32]*ServerPeerConn
type ServerRouteMap = map[uint32]*ServerRoute

type Server struct {
	ctx             context.Context
	ctx_cancel      context.CancelFunc
	ctltlscfg       *tls.Config
	rpctlscfg       *tls.Config

	wg              sync.WaitGroup
	stop_req        atomic.Bool
	stop_chan       chan bool

	ext_mtx         sync.Mutex
	ext_svcs        []Service

	ctl_addr        []string
	ctl_prefix      string
	ctl_mux         *http.ServeMux
	ctl             []*http.Server // control server

	rpc             []*net.TCPListener // main listener for grpc
	rpc_wg          sync.WaitGroup
	rpc_svr         *grpc.Server

	cts_limit       int
	cts_mtx         sync.Mutex
	cts_map         ServerConnMap
	cts_map_by_addr ServerConnMapByAddr
	cts_wg          sync.WaitGroup

	log             Logger

	stats struct {
		conns atomic.Int64
		routes atomic.Int64
		peers atomic.Int64
	}

	UnimplementedHoduServer
}

// connection from client.
// client connect to the server, the server accept it, and makes a tunnel request
type ServerConn struct {
	svr        *Server
	id          uint32
	sid         string // for logging

	remote_addr net.Addr // client address that created this structure
	local_addr  net.Addr // local address that the client is connected to
	pss        *GuardedPacketStreamServer

	route_mtx   sync.Mutex
	route_map   ServerRouteMap
	route_wg    sync.WaitGroup

	wg          sync.WaitGroup
	stop_req    atomic.Bool
	stop_chan   chan bool
}

type ServerRoute struct {
	cts        *ServerConn
	l          *net.TCPListener
	svc_addr   *net.TCPAddr // listening address
	svc_permitted_net netip.Prefix
	svc_proto   ROUTE_PROTO

	ptc_addr    string
	id          uint32

	pts_mtx     sync.Mutex
	pts_map     ServerPeerConnMap
	pts_limit   int
	pts_last_id uint32
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

func NewServerRoute(cts *ServerConn, id uint32, proto ROUTE_PROTO, ptc_addr string, svc_permitted_net string) (*ServerRoute, error) {
	var r ServerRoute
	var l *net.TCPListener
	var svcaddr *net.TCPAddr
	var svcnet netip.Prefix
	var err error

	if svc_permitted_net != "" {
		svcnet, err = netip.ParsePrefix(svc_permitted_net)
		if err != nil {
			return nil , err
		}
	}

	l, svcaddr, err = cts.make_route_listener(id, proto)
	if err != nil {
		return nil, err
	}

	if svc_permitted_net == "" {
		if svcaddr.IP.To4() != nil {
			svcnet, _ = netip.ParsePrefix("0.0.0.0/0")
		} else {
			svcnet, _ = netip.ParsePrefix("::/0")
		}
	}

	r.cts = cts
	r.id = id
	r.l = l
	r.svc_addr = svcaddr
	r.svc_permitted_net = svcnet
	r.svc_proto = proto

	r.ptc_addr = ptc_addr
	r.pts_limit = PTS_LIMIT
	r.pts_map = make(ServerPeerConnMap)
	r.pts_last_id = 0
	r.stop_req.Store(false)

	return &r, nil
}

func (r *ServerRoute) AddNewServerPeerConn(c *net.TCPConn) (*ServerPeerConn, error) {
	var pts *ServerPeerConn
	var ok bool
	var start_id uint32

	r.pts_mtx.Lock()
	defer r.pts_mtx.Unlock()

	if len(r.pts_map) >= r.pts_limit {
		return nil, fmt.Errorf("peer-to-server connection table full")
	}

	start_id = r.pts_last_id
	for {
		_, ok = r.pts_map[r.pts_last_id]
		if !ok {
			break
		}
		r.pts_last_id++
		if r.pts_last_id == start_id {
			// unlikely to happen but it cycled through the whole range.
			return nil, fmt.Errorf("failed to assign peer-to-server connection id")
		}
	}

	pts = NewServerPeerConn(r, c, r.pts_last_id)
	r.pts_map[pts.conn_id] = pts
	r.pts_last_id++
	r.cts.svr.stats.peers.Add(1)

	return pts, nil
}

func (r *ServerRoute) RemoveServerPeerConn(pts *ServerPeerConn) {
	r.pts_mtx.Lock()
	delete(r.pts_map, pts.conn_id)
	r.cts.svr.stats.peers.Add(-1)
	r.pts_mtx.Unlock()
	r.cts.svr.log.Write(r.cts.sid, LOG_DEBUG, "Removed server-side peer connection %s from route(%d)", pts.conn.RemoteAddr().String(), r.id)
}

func (r *ServerRoute) RunTask(wg *sync.WaitGroup) {
	var err error
	var conn *net.TCPConn
	var pts *ServerPeerConn
	var raddr *net.TCPAddr
	var iaddr netip.Addr

	defer wg.Done()

	for {
		conn, err = r.l.AcceptTCP()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				r.cts.svr.log.Write(r.cts.sid, LOG_INFO, "Server-side peer listener closed on route(%d)", r.id)
			} else {
				r.cts.svr.log.Write(r.cts.sid, LOG_INFO, "Server-side peer listener error on route(%d) - %s", r.id, err.Error())
			}
			break
		}

		raddr = conn.RemoteAddr().(*net.TCPAddr)
		iaddr, _ = netip.AddrFromSlice(raddr.IP)

		if !r.svc_permitted_net.Contains(iaddr) {
			r.cts.svr.log.Write(r.cts.sid, LOG_DEBUG, "Rejected server-side peer %s to route(%d) - allowed range %v", raddr.String(), r.id, r.svc_permitted_net)
			conn.Close()
		}

		pts, err = r.AddNewServerPeerConn(conn)
		if err != nil {
			r.cts.svr.log.Write(r.cts.sid, LOG_ERROR, "Failed to add server-side peer %s to route(%d) - %s", r.id, raddr.String(), r.id, err.Error())
			conn.Close()
		} else {
			r.cts.svr.log.Write(r.cts.sid, LOG_DEBUG, "Added server-side peer %s to route(%d)", raddr.String(), r.id)
			r.pts_wg.Add(1)
			go pts.RunTask(&r.pts_wg)
		}
	}

	r.ReqStop()

	r.pts_wg.Wait()
	r.cts.svr.log.Write(r.cts.sid, LOG_DEBUG, "All service-side peer handlers completed on route(%d)", r.id)

	r.cts.RemoveServerRoute(r) // final phase...
}

func (r *ServerRoute) ReqStop() {
	if r.stop_req.CompareAndSwap(false, true) {
		var pts *ServerPeerConn

		for _, pts = range r.pts_map {
			pts.ReqStop()
		}

		r.l.Close()
	}
}

func (r *ServerRoute) ReportEvent(pts_id uint32, event_type PACKET_KIND, event_data interface{}) error {
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

func (cts *ServerConn) make_route_listener(id uint32, proto ROUTE_PROTO) (*net.TCPListener, *net.TCPAddr, error) {
	var l *net.TCPListener
	var err error
	var svcaddr *net.TCPAddr
	var port int
	var tries int = 0
	var nw string
	var ip string

	switch proto {
		case ROUTE_PROTO_TCP:
			nw = "tcp"
			ip = ""
		case ROUTE_PROTO_TCP4:
			nw = "tcp4"
			ip = "0.0.0.0"
		case ROUTE_PROTO_TCP6:
			nw = "tcp6"
			ip = "[::]"
		default:
			return nil, nil, fmt.Errorf("invalid protocol number %d", proto)
	}

	for {
		port = rand.Intn(65535-32000+1) + 32000 // TODO: configurable port range

		svcaddr, err = net.ResolveTCPAddr(nw, fmt.Sprintf("%s:%d", ip, port))
		if err == nil {
			l, err = net.ListenTCP(nw, svcaddr) // make the binding address configurable. support multiple binding addresses???
			if err == nil {
				cts.svr.log.Write(cts.sid, LOG_DEBUG, "Route(%d) listening on %d", id, port)
				return l, svcaddr, nil
			}
		}

		tries++
		if tries >= 1000 {
			err = fmt.Errorf("unable to allocate port")
			break
		}
	}

	return nil, nil, err
}

func (cts *ServerConn) AddNewServerRoute(route_id uint32, proto ROUTE_PROTO, ptc_addr string, svc_permitted_net string) (*ServerRoute, error) {
	var r *ServerRoute
	var err error

	cts.route_mtx.Lock()
	if cts.route_map[route_id] != nil {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf("existent route id - %d", route_id)
	}
	r, err = NewServerRoute(cts, route_id, proto, ptc_addr, svc_permitted_net)
	if err != nil {
		cts.route_mtx.Unlock()
		return nil, err
	}
	cts.route_map[route_id] = r
	cts.svr.stats.routes.Add(1)
	cts.route_mtx.Unlock()

	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ServerConn) RemoveServerRoute(route *ServerRoute) error {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route.id]
	if !ok {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route id - %d", route.id)
	}
	if r != route {
		cts.route_mtx.Unlock()
		return fmt.Errorf("non-existent route - %d", route.id)
	}
	delete(cts.route_map, route.id)
	cts.svr.stats.routes.Add(-1)
	cts.route_mtx.Unlock()

	r.ReqStop()
	return nil
}

func (cts *ServerConn) RemoveServerRouteById(route_id uint32) (*ServerRoute, error) {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if !ok {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf("non-existent route id - %d", route_id)
	}
	delete(cts.route_map, route_id)
	cts.svr.stats.routes.Add(-1)
	cts.route_mtx.Unlock()

	r.ReqStop()
	return r, nil
}

func (cts *ServerConn) ReportEvent(route_id uint32, pts_id uint32, event_type PACKET_KIND, event_data interface{}) error {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if (!ok) {
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
			cts.svr.log.Write(cts.sid, LOG_INFO, "RPC stream closed for client %s", cts.remote_addr)
			goto done
		}
		if err != nil {
			cts.svr.log.Write(cts.sid, LOG_ERROR, "RPC stream error for client %s - %s", cts.remote_addr, err.Error())
			goto done
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_START:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r *ServerRoute

					r, err = cts.AddNewServerRoute(x.Route.RouteId, x.Route.ServiceProto, x.Route.TargetAddrStr, x.Route.ServiceNetStr)
					if err != nil {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Failed to add route(%d,%s) for %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr, err.Error())

						err = cts.pss.Send(MakeRouteStoppedPacket(x.Route.RouteId, x.Route.ServiceProto, x.Route.TargetAddrStr, x.Route.ServiceNetStr))
						if err != nil {
							cts.svr.log.Write(cts.sid, LOG_ERROR,
								"Failed to send route_stopped event(%d,%s,%v,%s) to client %s - %s",
								x.Route.RouteId, x.Route.TargetAddrStr,  x.Route.ServiceProto, x.Route.ServiceNetStr, cts.remote_addr, err.Error())
							goto done
						} else {
							cts.svr.log.Write(cts.sid, LOG_DEBUG,
								"Sent route_stopped event(%d,%s,%v,%s) to client %s",
								x.Route.RouteId, x.Route.TargetAddrStr,  x.Route.ServiceProto, x.Route.ServiceNetStr, cts.remote_addr)
						}

					} else {
						cts.svr.log.Write(cts.sid, LOG_INFO,
							"Added route(%d,%s,%s,%v,%v) for client %s to cts(%d)",
							r.id, r.ptc_addr, r.svc_addr.String(), r.svc_proto, r.svc_permitted_net, cts.remote_addr, cts.id)
						err = cts.pss.Send(MakeRouteStartedPacket(r.id, r.svc_proto, r.svc_addr.String(), r.svc_permitted_net.String()))
						if err != nil {
							r.ReqStop()
							cts.svr.log.Write(cts.sid, LOG_ERROR,
								"Failed to send route_started event(%d,%s,%s,%s%v,%v) to client %s - %s",
								r.id, r.ptc_addr, r.svc_addr.String(), r.svc_proto, r.svc_permitted_net, cts.remote_addr, err.Error())
							goto done
						}
					}
				} else {
					cts.svr.log.Write(cts.sid, LOG_INFO, "Received invalid packet from %s", cts.remote_addr)
					// TODO: need to abort this client?
				}

			case PACKET_KIND_ROUTE_STOP:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r *ServerRoute

					r, err = cts.RemoveServerRouteById(x.Route.RouteId)
					if err != nil {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Failed to delete route(%d,%s) for client %s - %s",
							x.Route.RouteId, x.Route.TargetAddrStr, cts.remote_addr, err.Error())
					} else {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Deleted route(%d,%s,%s,%v,%v) for client %s",
							r.id, r.ptc_addr, r.svc_addr.String(), r.svc_proto, r.svc_permitted_net.String(), cts.remote_addr)
						err = cts.pss.Send(MakeRouteStoppedPacket(r.id, r.svc_proto, r.ptc_addr, r.svc_permitted_net.String()))
						if err != nil {
							r.ReqStop()
							cts.svr.log.Write(cts.sid, LOG_ERROR,
								"Failed to send route_stopped event(%d,%s,%s,%v.%v) to client %s - %s",
								r.id, r.ptc_addr, r.svc_addr.String(), r.svc_proto, r.svc_permitted_net.String(), cts.remote_addr, err.Error())
							goto done
						}
					}
				} else {
					cts.svr.log.Write(cts.sid, LOG_ERROR, "Invalid route_stop event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_STARTED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STARTED, x.Peer)
					if err != nil {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_started event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.svr.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_started event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					// invalid event data
					cts.svr.log.Write(cts.sid, LOG_ERROR, "Invalid peer_started event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_ABORTED:
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_ABORTED, x.Peer)
					if err != nil {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_aborted event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.svr.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_aborted event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					// invalid event data
					cts.svr.log.Write(cts.sid, LOG_ERROR, "Invalid peer_aborted event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_STOPPED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STOPPED, x.Peer)
					if err != nil {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_stopped event from %s for peer(%d,%d,%s,%s) - %s",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr, err.Error())
					} else {
						cts.svr.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_stopped event from %s for peer(%d,%d,%s,%s)",
							cts.remote_addr, x.Peer.RouteId, x.Peer.PeerId, x.Peer.LocalAddrStr, x.Peer.RemoteAddrStr)
					}
				} else {
					// invalid event data
					cts.svr.log.Write(cts.sid, LOG_ERROR, "Invalid peer_stopped event from %s", cts.remote_addr)
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(x.Data.RouteId, x.Data.PeerId, PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
						cts.svr.log.Write(cts.sid, LOG_ERROR,
							"Failed to handle peer_data event from %s for peer(%d,%d) - %s",
							cts.remote_addr, x.Data.RouteId, x.Data.PeerId, err.Error())
					} else {
						cts.svr.log.Write(cts.sid, LOG_DEBUG,
							"Handled peer_data event from %s for peer(%d,%d)",
							cts.remote_addr, x.Data.RouteId, x.Data.PeerId)
					}
				} else {
					// invalid event data
					cts.svr.log.Write(cts.sid, LOG_ERROR, "Invalid peer_data event from %s", cts.remote_addr)
				}
		}
	}

done:
	cts.svr.log.Write(cts.sid, LOG_INFO, "RPC stream receiver ended")
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
				cts.svr.log.Write(cts.sid, LOG_INFO, "RPC stream done - %s", ctx.Err().Error())
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
}

func (cts *ServerConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		var r *ServerRoute

		for _, r = range cts.route_map {
			r.ReqStop()
		}

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
			cc.server.log.Write("", LOG_INFO, "Client disconnected - %s", addr)
			cc.server.RemoveServerConnByAddr(p.Addr)
	}
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


type server_ctl_log_writer struct {
	svr *Server
}

func (hlw *server_ctl_log_writer) Write(p []byte) (n int, err error) {
	// the standard http.Server always requires *log.Logger
	// use this iowriter to create a logger to pass it to the http server.
	hlw.svr.log.Write("", LOG_INFO, string(p))
	return len(p), nil
}

func NewServer(ctx context.Context, ctl_addrs []string, rpc_addrs []string, logger Logger, ctl_prefix string, ctltlscfg *tls.Config, rpctlscfg *tls.Config) (*Server, error) {
	var s Server
	var l *net.TCPListener
	var rpcaddr *net.TCPAddr
	var err error
	var addr string
	var gl *net.TCPListener
	var i int
	var cwd string
	var hs_log *log.Logger
	var opts []grpc.ServerOption

	if len(rpc_addrs) <= 0 {
		return nil, fmt.Errorf("no server addresses provided")
	}

	s.ctx, s.ctx_cancel = context.WithCancel(ctx)
	s.log = logger
	/* create the specified number of listeners */
	s.rpc = make([]*net.TCPListener, 0)
	for _, addr = range rpc_addrs {
		rpcaddr, err = net.ResolveTCPAddr(NET_TYPE_TCP, addr) // Make this interruptable???
		if err != nil {
			goto oops
		}

		l, err = net.ListenTCP(NET_TYPE_TCP, rpcaddr)
		if err != nil {
			goto oops
		}

		s.rpc = append(s.rpc, l)
	}

	s.ctltlscfg = ctltlscfg
	s.rpctlscfg = rpctlscfg
	s.ext_svcs = make([]Service, 0, 1)
	s.cts_limit = CTS_LIMIT // TODO: accept this from configuration
	s.cts_map = make(ServerConnMap)
	s.cts_map_by_addr = make(ServerConnMapByAddr)
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
	if s.rpctlscfg != nil { opts = append(opts, grpc.Creds(credentials.NewTLS(s.rpctlscfg))) }
	//opts = append(opts, grpc.UnaryInterceptor(unaryInterceptor))
	//opts = append(opts, grpc.StreamInterceptor(streamInterceptor))
	s.rpc_svr = grpc.NewServer(opts...)
	RegisterHoduServer(s.rpc_svr, &s)

	s.ctl_prefix = ctl_prefix

	s.ctl_mux = http.NewServeMux()
	cwd, _ = os.Getwd()
	s.ctl_mux.Handle(s.ctl_prefix + "/ui/", http.StripPrefix(s.ctl_prefix, http.FileServer(http.Dir(cwd)))) // TODO: proper directory. it must not use the current working directory...
	s.ctl_mux.Handle(s.ctl_prefix + "/ws/tty", new_server_ctl_ws_tty(&s))
	s.ctl_mux.Handle(s.ctl_prefix + "/server-conns", &server_ctl_server_conns{s: &s})
	s.ctl_mux.Handle(s.ctl_prefix + "/server-conns/{conn_id}", &server_ctl_server_conns_id{s: &s})
	s.ctl_mux.Handle(s.ctl_prefix + "/server-conns/{conn_id}/routes", &server_ctl_server_conns_id_routes{s: &s})
	s.ctl_mux.Handle(s.ctl_prefix + "/server-conns/{conn_id}/routes/{route_id}", &server_ctl_server_conns_id_routes_id{s: &s})
	s.ctl_mux.Handle(s.ctl_prefix + "/stats", &server_ctl_stats{s: &s})

	s.ctl_addr = make([]string, len(ctl_addrs))
	s.ctl = make([]*http.Server, len(ctl_addrs))
	copy(s.ctl_addr, ctl_addrs)
	hs_log = log.New(&server_ctl_log_writer{svr: &s}, "", 0);

	for i = 0; i < len(ctl_addrs); i++ {
		s.ctl[i] = &http.Server{
			Addr: ctl_addrs[i],
			Handler: s.ctl_mux,
			TLSConfig: s.ctltlscfg,
			ErrorLog: hs_log,
			// TODO: more settings
		}
	}

	s.stats.conns.Store(0)
	s.stats.routes.Store(0)
	s.stats.peers.Store(0)

	return &s, nil

oops:
	// TODO: check if rpc_svr needs to be closed. probably not. closing the listen may be good enough
	if gl != nil {
		gl.Close()
	}

	for _, l = range s.rpc {
		l.Close()
	}
	s.rpc = make([]*net.TCPListener, 0)
	return nil, err
}

func (s *Server) run_grpc_server(idx int, wg *sync.WaitGroup) error {
	var l *net.TCPListener
	var err error

	defer wg.Done()

	l = s.rpc[idx]
	// it seems to be safe to call a single grpc server on differnt listening sockets multiple times
	s.log.Write("", LOG_ERROR, "Starting RPC server on %s", l.Addr().String())
	err = s.rpc_svr.Serve(l)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			s.log.Write("", LOG_ERROR, "RPC server on %s closed", l.Addr().String())
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
	s.log.Write("", LOG_DEBUG, "All RPC listeners completed")

	s.cts_wg.Wait()
	s.log.Write("", LOG_DEBUG, "All CTS handlers completed")

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

			s.log.Write ("", LOG_INFO, "Control channel[%d] started on %s", i, s.ctl_addr[i])

			// defeat hard-coded "tcp" in ListenAndServe() and ListenAndServeTLS()
			//  err = cs.ListenAndServe()
			//  err = cs.ListenAndServeTLS("", "")
			l, err = net.Listen(tcp_addr_str_class(cs.Addr), cs.Addr)
			if err == nil {
				if s.ctltlscfg == nil {
					err = cs.Serve(l)
				} else {
					err = cs.ServeTLS(l, "", "") // s.ctltlscfg must provide a certificate and a key
				}
				l.Close()
			}
			if errors.Is(err, http.ErrServerClosed) {
				s.log.Write("", LOG_DEBUG, "Control channel[%d] ended", i)
			} else {
				s.log.Write("", LOG_ERROR, "Control channel[%d] error - %s", i, err.Error())
			}
			l_wg.Done()
		}(idx, ctl)
	}
	l_wg.Wait()
}

func (s *Server) ReqStop() {
	if s.stop_req.CompareAndSwap(false, true) {
		var l *net.TCPListener
		var cts *ServerConn
		var ctl *http.Server

		for _, ctl = range s.ctl {
			ctl.Shutdown(s.ctx) // to break c.ctl.ListenAndServe()
		}

		//s.rpc_svr.GracefulStop()
		//s.rpc_svr.Stop()
		for _, l = range s.rpc {
			l.Close()
		}

		s.cts_mtx.Lock()
		for _, cts = range s.cts_map {
			cts.ReqStop() // request to stop connections from/to peer held in the cts structure
		}
		s.cts_mtx.Unlock()

		s.stop_chan <- true
		s.ctx_cancel()
	}
}

func (s *Server) AddNewServerConn(remote_addr *net.Addr, local_addr *net.Addr, pss Hodu_PacketStreamServer) (*ServerConn, error) {
	var cts ServerConn
	var id uint32
	var ok bool

	cts.svr = s
	cts.route_map = make(ServerRouteMap)
	cts.remote_addr = *remote_addr
	cts.local_addr = *local_addr
	cts.pss = &GuardedPacketStreamServer{Hodu_PacketStreamServer: pss}

	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 8)

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	if len(s.cts_map) > s.cts_limit {
		return nil, fmt.Errorf("too many connections - %d", s.cts_limit)
	}

	id = rand.Uint32()
	for {
		_, ok = s.cts_map[id]
		if !ok { break }
		id++
	}
	cts.id = id
	cts.sid = fmt.Sprintf("%d", id) // id in string used for logging

	_, ok = s.cts_map_by_addr[cts.remote_addr]
	if ok {
		return nil, fmt.Errorf("existing client - %s", cts.remote_addr.String())
	}
	s.cts_map_by_addr[cts.remote_addr] = &cts
	s.cts_map[id] = &cts;
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.log.Write("", LOG_DEBUG, "Added client connection from %s", cts.remote_addr.String())
	return &cts, nil
}

func (s *Server) ReqStopAllServerConns() {
	var cts *ServerConn

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	for _, cts = range s.cts_map {
		cts.ReqStop()
	}
}

func (s *Server) RemoveServerConn(cts *ServerConn) error {
	var conn *ServerConn
	var ok bool

	s.cts_mtx.Lock()

	conn, ok = s.cts_map[cts.id]
	if !ok {
		s.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.id)
	}
	if conn != cts {
		s.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection id - %d", cts.id)
	}

	delete(s.cts_map, cts.id)
	delete(s.cts_map_by_addr, cts.remote_addr)
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.cts_mtx.Unlock()

	cts.ReqStop()
	return nil
}

func (s *Server) RemoveServerConnByAddr(addr net.Addr) error {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()

	cts, ok = s.cts_map_by_addr[addr]
	if !ok {
		s.cts_mtx.Unlock()
		return fmt.Errorf("non-existent connection address - %s", addr.String())
	}
	delete(s.cts_map, cts.id)
	delete(s.cts_map_by_addr, cts.remote_addr)
	s.stats.conns.Store(int64(len(s.cts_map)))
	s.cts_mtx.Unlock()

	cts.ReqStop()
	return nil
}

func (s* Server) FindServerConnById(id uint32) *ServerConn {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map[id]
	if !ok {
		return nil
	}

	return cts
}

func (s *Server) FindServerConnByAddr(addr net.Addr) *ServerConn {
	var cts *ServerConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map_by_addr[addr]
	if !ok {
		return nil
	}

	return cts
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

func (s *Server) StopServices() {
	var ext_svc Service
	s.ReqStop()
	for _, ext_svc = range s.ext_svcs {
		ext_svc.StopServices()
	}
}

func (s *Server) WaitForTermination() {
	s.wg.Wait()
}

func (s *Server) WriteLog(id string, level LogLevel, fmtstr string, args ...interface{}) {
	s.log.Write(id, level, fmtstr, args...)
}
