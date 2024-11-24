package hodu

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "io"
import "math/rand"
import "net"
import "net/http"
import "sync"
import "sync/atomic"
//import "time"

import "google.golang.org/grpc"
//import "google.golang.org/grpc/metadata"
import "google.golang.org/grpc/peer"
import "google.golang.org/grpc/stats"

const PTS_LIMIT = 8192
//const CTS_LIMIT = 2048

type ClientConnMap = map[net.Addr]*ClientConn
type ServerPeerConnMap = map[uint32]*ServerPeerConn
type ServerRouteMap = map[uint32]*ServerRoute

type Server struct {
	ctx         context.Context
	ctx_cancel  context.CancelFunc
	tlscfg      *tls.Config

	wg          sync.WaitGroup
	ext_svcs    []Service
	stop_req    atomic.Bool
	stop_chan   chan bool

	ctl         *http.Server // control server

	l           []*net.TCPListener // main listener for grpc
	l_wg        sync.WaitGroup

	cts_mtx     sync.Mutex
	cts_map     ClientConnMap
	cts_wg      sync.WaitGroup

	gs          *grpc.Server
	log         Logger

	UnimplementedHoduServer
}

// client connection to server.
// client connect to the server, the server accept it, and makes a tunnel request
type ClientConn struct {
	svr       *Server
	caddr      net.Addr // client address that created this structure
	pss       *GuardedPacketStreamServer

	route_mtx  sync.Mutex
	route_map  ServerRouteMap
	route_wg   sync.WaitGroup

	wg         sync.WaitGroup
	stop_req   atomic.Bool
	stop_chan  chan bool
}

type ServerRoute struct {
	cts        *ClientConn
	l          *net.TCPListener
	laddr      *net.TCPAddr
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

func NewServerRoute(cts *ClientConn, id uint32, proto ROUTE_PROTO) (*ServerRoute, error) {
	var r ServerRoute
	var l *net.TCPListener
	var laddr *net.TCPAddr
	var err error

	l, laddr, err = cts.make_route_listener(proto)
	if err != nil {
		return nil, err
	}

	r.cts = cts
	r.id = id
	r.l = l
	r.laddr = laddr
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

	return pts, nil
}

func (r *ServerRoute) RemoveServerPeerConn(pts *ServerPeerConn) {
	r.pts_mtx.Lock()
	delete(r.pts_map, pts.conn_id)
	r.pts_mtx.Unlock()
	r.cts.svr.log.Write("", LOG_DEBUG, "Removed server-side peer connection %s", pts.conn.RemoteAddr().String())
}

func (r *ServerRoute) RunTask(wg *sync.WaitGroup) {
	var err error
	var conn *net.TCPConn
	var pts *ServerPeerConn
	var log_id string

	defer wg.Done()
	log_id = fmt.Sprintf("%s,%d", r.cts.caddr.String(), r.id)

	for {
		conn, err = r.l.AcceptTCP()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				r.cts.svr.log.Write(log_id, LOG_INFO, "Server-side peer listener closed")
			} else {
				r.cts.svr.log.Write(log_id, LOG_INFO, "Server-side peer listener error - %s", err.Error())
			}
			break
		}

		pts, err = r.AddNewServerPeerConn(conn)
		if err != nil {
			r.cts.svr.log.Write(log_id, LOG_ERROR, "Failed to add new server-side peer %s - %s", conn.RemoteAddr().String(), err.Error())
			conn.Close()
		} else {
			r.cts.svr.log.Write(log_id, LOG_DEBUG, "Added new server-side peer %s", conn.RemoteAddr().String())
			r.pts_wg.Add(1)
			go pts.RunTask(&r.pts_wg)
		}
	}

	r.ReqStop()

	r.pts_wg.Wait()
	r.cts.svr.log.Write(log_id, LOG_DEBUG, "All service-side peer handlers completed")

	r.cts.RemoveServerRoute(r) // final phase...
}

func (r *ServerRoute) ReqStop() {
	fmt.Printf ("requesting to stop route taak..\n")

	if r.stop_req.CompareAndSwap(false, true) {
		var pts *ServerPeerConn

		for _, pts = range r.pts_map {
			pts.ReqStop()
		}

		r.l.Close()
	}
	fmt.Printf ("requiested to stopp route taak..\n")
}

func (r *ServerRoute) ReportEvent (pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	var spc *ServerPeerConn
	var ok bool

	r.pts_mtx.Lock()
	spc, ok = r.pts_map[pts_id]
	if !ok {
		r.pts_mtx.Unlock()
		return fmt.Errorf("non-existent peer id - %u", pts_id)
	}
	r.pts_mtx.Unlock()

	return spc.ReportEvent(event_type, event_data)
}
// ------------------------------------

func (cts *ClientConn) make_route_listener(proto ROUTE_PROTO) (*net.TCPListener, *net.TCPAddr, error) {
	var l *net.TCPListener
	var err error
	var laddr *net.TCPAddr
	var port int
	var tries int = 0
	var nw string

	switch proto {
		case ROUTE_PROTO_TCP:
			nw = "tcp"
		case ROUTE_PROTO_TCP4:
			nw = "tcp4"
		case ROUTE_PROTO_TCP6:
			nw = "tcp6"
	}

	for {
		port = rand.Intn(65535-32000+1) + 32000

		laddr, err = net.ResolveTCPAddr(nw, fmt.Sprintf(":%d", port))
		if err == nil {
			l, err = net.ListenTCP(nw, laddr) // make the binding address configurable. support multiple binding addresses???
			if err == nil {
				fmt.Printf("listening  .... on ... %d\n", port)
				return l, laddr, nil
			}
		}

		// TODO: implement max retries..
		tries++
		if tries >= 1000 {
			err = fmt.Errorf("unable to allocate port")
			break
		}
	}

	return nil, nil, err
}

func (cts *ClientConn) AddNewServerRoute(route_id uint32, proto ROUTE_PROTO) (*ServerRoute, error) {
	var r *ServerRoute
	var err error

	cts.route_mtx.Lock()
	if cts.route_map[route_id] != nil {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf ("existent route id - %d", route_id)
	}
	r, err = NewServerRoute(cts, route_id, proto)
	if err != nil {
		cts.route_mtx.Unlock()
		return nil, err
	}
	cts.route_map[route_id] = r
	cts.route_mtx.Unlock()

	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ClientConn) RemoveServerRoute (route* ServerRoute) error {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route.id]
	if (!ok) {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route id - %d", route.id)
	}
	if (r != route) {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route - %d", route.id)
	}
	delete(cts.route_map, route.id)
	cts.route_mtx.Unlock()

	r.ReqStop()
	return nil
}

func (cts *ClientConn) RemoveServerRouteById (route_id uint32) (*ServerRoute, error) {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if (!ok) {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf ("non-existent route id - %d", route_id)
	}
	delete(cts.route_map, route_id)
	cts.route_mtx.Unlock()

	r.ReqStop()
	return r, nil
}

func (cts *ClientConn) ReportEvent (route_id uint32, pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if (!ok) {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route id - %d", route_id)
	}
	cts.route_mtx.Unlock()

	return r.ReportEvent(pts_id, event_type, event_data)
}

func (cts *ClientConn) receive_from_stream(wg *sync.WaitGroup) {
	var pkt *Packet
	var err error

	defer wg.Done()

	for {
		pkt, err = cts.pss.Recv()
		if errors.Is(err, io.EOF) {
			cts.svr.log.Write("", LOG_INFO, "GRPC stream closed for client %s", cts.caddr)
			goto done
		}
		if err != nil {
			cts.svr.log.Write("", LOG_ERROR, "GRPC stream error for client %s - %s", cts.caddr, err.Error())
			goto done
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_START:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r* ServerRoute

					r, err = cts.AddNewServerRoute(x.Route.RouteId, x.Route.Proto)
					if err != nil {
						cts.svr.log.Write("", LOG_ERROR, "Failed to add server route for client %s peer %s", cts.caddr, x.Route.AddrStr)
					} else {
						cts.svr.log.Write("", LOG_INFO, "Added server route(id=%d) for client %s peer %s", r.id, cts.caddr, x.Route.AddrStr)
						err = cts.pss.Send(MakeRouteStartedPacket(r.id, x.Route.Proto, r.laddr.String()))
						if err != nil {
							r.ReqStop()
							cts.svr.log.Write("", LOG_ERROR, "Failed to inform client %s of server route started for peer %s", cts.caddr, x.Route.AddrStr)
							goto done
						}
					}
				} else {
					cts.svr.log.Write("", LOG_INFO, "Received invalid packet from %s", cts.caddr)
					// TODO: need to abort this client?
				}

			case PACKET_KIND_ROUTE_STOP:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r* ServerRoute

					r, err = cts.RemoveServerRouteById(x.Route.RouteId)
					if err != nil {
						cts.svr.log.Write("", LOG_ERROR, "Failed to delete server route(id=%d) for client %s peer %s", x.Route.RouteId, cts.caddr, x.Route.AddrStr)
					} else {
						cts.svr.log.Write("", LOG_ERROR, "Deleted server route(id=%d) for client %s peer %s", x.Route.RouteId, cts.caddr, x.Route.AddrStr)
						err = cts.pss.Send(MakeRouteStoppedPacket(x.Route.RouteId, x.Route.Proto))
						if err != nil {
							r.ReqStop()
							cts.svr.log.Write("", LOG_ERROR, "Failed to inform client %s of server route(id=%d) stopped for peer %s", cts.caddr, x.Route.RouteId, x.Route.AddrStr)
							goto done
						}
					}
				} else {
					cts.svr.log.Write("", LOG_INFO, "Received invalid packet from %s", cts.caddr)
					// TODO: need to abort this client?
				}

			case PACKET_KIND_PEER_STARTED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STARTED, nil)
					if err != nil {
						// TODO:
						fmt.Printf ("Failed to report PEER_STARTED Event")
					} else {
						// TODO:
					}
				} else {
					// TODO
				}

			case PACKET_KIND_PEER_ABORTED:
				fallthrough
			case PACKET_KIND_PEER_STOPPED:
				// the connection from the client to a peer has been established
				var x *Packet_Peer
				var ok bool
				x, ok = pkt.U.(*Packet_Peer)
				if ok {
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STOPPED, nil)
					if err != nil {
						// TODO:
						fmt.Printf ("Failed to report PEER_STOPPED Event")
					} else {
						// TODO:
					}
				} else {
					// TODO
				}

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(x.Data.RouteId, x.Data.PeerId, PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
						// TODO:
						fmt.Printf ("Failed to report PEER_DATA Event")
					} else {
						// TODO:
					}
				} else {
					// TODO
				}
		}
	}

done:
	fmt.Printf ("************ stream receiver finished....\n")
}

func (cts *ClientConn) RunTask(wg *sync.WaitGroup) {
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
fmt.Printf("grpc server done - %s\n", ctx.Err().Error())
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
fmt.Printf ("^^^^^^^^^^^^^^^^^ waiting for reoute_wg...\n")
	cts.ReqStop() // just in case
	cts.route_wg.Wait()
fmt.Printf ("^^^^^^^^^^^^^^^^^ waited for reoute_wg...\n")
}

func (cts *ClientConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		var r *ServerRoute

		for _, r = range cts.route_map {
			r.ReqStop()
		}

		// there is no good way to break a specific connection client to
		// the grpc server. while the global grpc server is closed in
		// ReqStop() for Server, the individuation connection is closed
		// by returing from the grpc handler goroutine. See the comment
		// RunTask() for ClientConn.
		cts.stop_chan <- true
	}
}

// --------------------------------------------------------------------

func (s *Server) GetSeed (ctx context.Context, c_seed *Seed) (*Seed, error) {
	var s_seed Seed

	// seed exchange is for furture expansion of the protocol
	// there is nothing to do much about it for now.

	s_seed.Version = HODU_VERSION
	s_seed.Flags = 0

	// we create no ClientConn structure associated with the connection
	// at this phase for the server. it doesn't track the client version and
	// features. we delegate protocol selection solely to the client.

	return &s_seed, nil
}

func (s *Server) PacketStream(strm Hodu_PacketStreamServer) error {
	var ctx context.Context
	var p *peer.Peer
	var ok bool
	var err error
	var cts *ClientConn

	ctx = strm.Context()
	p, ok = peer.FromContext(ctx)
	if (!ok) {
		return fmt.Errorf("failed to get peer from packet stream context")
	}

	cts, err = s.AddNewClientConn(p.Addr, strm)
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
//    fmt.Println(ctx.Value("user_id")) // Returns nil, can't access the value
	var p *peer.Peer
	var ok bool
	var addr string

	p, ok = peer.FromContext(ctx)
	if (!ok) {
		addr = ""
	} else {
		addr = p.Addr.String()
	}
/*
md,ok:=metadata.FromIncomingContext(ctx)
fmt.Printf("%+v%+v\n",md,ok)
if ok {
}*/
	switch cs.(type) {
		case *stats.ConnBegin:
			fmt.Printf("**** client connected - [%s]\n", addr)
		case *stats.ConnEnd:
			fmt.Printf("**** client disconnected - [%s]\n", addr)
			cc.server.RemoveClientConnByAddr(p.Addr)
    }
}

// wrappedStream wraps around the embedded grpc.ServerStream, and intercepts the RecvMsg and
// SendMsg method call.
type wrappedStream struct {
	grpc.ServerStream
}

func (w *wrappedStream) RecvMsg(m any) error {
	//fmt.Printf("Receive a message (Type: %T) at %s\n", m, time.Now().Format(time.RFC3339))
	return w.ServerStream.RecvMsg(m)
}

func (w *wrappedStream) SendMsg(m any) error {
	//fmt.Printf("Send a message (Type: %T) at %v\n", m, time.Now().Format(time.RFC3339))
	return w.ServerStream.SendMsg(m)
}

func newWrappedStream(s grpc.ServerStream) grpc.ServerStream {
	return &wrappedStream{s}
}

func streamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	// authentication (token verification)
/*
	md, ok := metadata.FromIncomingContext(ss.Context())
	if !ok {
		return errMissingMetadata
	}
	if !valid(md["authorization"]) {
		return errInvalidToken
	}
*/

	err := handler(srv, newWrappedStream(ss))
	if err != nil {
		fmt.Printf("RPC failed with error: %v\n", err)
	}
	return err
}

func unaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	// authentication (token verification)
/*
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, errMissingMetadata
	}
	if !valid(md["authorization"]) {
//		return nil, errInvalidToken
	}
*/
	m, err := handler(ctx, req)
	if err != nil {
		fmt.Printf("RPC failed with error: %v\n", err)
	}

	return m, err
}

func NewServer(ctx context.Context, laddrs []string, logger Logger, tlscfg *tls.Config) (*Server, error) {
	var s Server
	var l *net.TCPListener
	var laddr *net.TCPAddr
	var err error
	var addr string
	var gl *net.TCPListener

	if len(laddrs) <= 0 {
		return nil, fmt.Errorf("no server addresses provided")
	}

	s.ctx, s.ctx_cancel = context.WithCancel(ctx)
	s.log = logger
	/* create the specified number of listeners */
	s.l = make([]*net.TCPListener, 0)
	for _, addr = range laddrs {
		laddr, err = net.ResolveTCPAddr(NET_TYPE_TCP, addr) // Make this interruptable???
		if err != nil {
			goto oops
		}

		l, err = net.ListenTCP(NET_TYPE_TCP, laddr)
		if err != nil {
			goto oops
		}

		s.l = append(s.l, l)
	}

	s.tlscfg = tlscfg
	s.ext_svcs = make([]Service, 0, 1)
	s.cts_map = make(ClientConnMap) // TODO: make it configurable...
	s.stop_chan = make(chan bool, 8)
	s.stop_req.Store(false)
/*
	creds, err := credentials.NewServerTLSFromFile(data.Path("x509/server_cert.pem"), data.Path("x509/server_key.pem"))
	if err != nil {
	  log.Fatalf("failed to create credentials: %v", err)
	}
	gs = grpc.NewServer(grpc.Creds(creds))
*/
	s.gs = grpc.NewServer(
		grpc.UnaryInterceptor(unaryInterceptor),
		grpc.StreamInterceptor(streamInterceptor),
		grpc.StatsHandler(&ConnCatcher{ server: &s }),
	) // TODO: have this outside the server struct?
	RegisterHoduServer (s.gs, &s)

	return &s, nil

oops:
/* TODO: check if gs needs to be closed... */
	if gl != nil {
		gl.Close()
	}

	for _, l = range s.l {
		l.Close()
	}
	s.l = make([]*net.TCPListener, 0)
	return nil, err
}

func (s *Server) run_grpc_server(idx int, wg *sync.WaitGroup) error {
	var l *net.TCPListener
	var err error

	defer wg.Done()

	l = s.l[idx]
	// it seems to be safe to call a single grpc server on differnt listening sockets multiple times
	s.log.Write ("", LOG_ERROR, "Starting GRPC server listening on %s", l.Addr().String())
	err = s.gs.Serve(l)
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			s.log.Write ("", LOG_ERROR, "GRPC server listening on %s closed", l.Addr().String())
		} else {
			s.log.Write ("", LOG_ERROR, "Error from GRPC server listening on %s - %s", l.Addr().String(), err.Error())
		}
		return err
	}

	return nil
}

func (s *Server) RunTask(wg *sync.WaitGroup) {
	var idx int

	defer wg.Done()

	for idx, _ = range s.l {
		s.l_wg.Add(1)
		go s.run_grpc_server(idx, &s.l_wg)
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

	s.l_wg.Wait()
	s.log.Write("", LOG_DEBUG, "All GRPC listeners completed")

	s.cts_wg.Wait()
	s.log.Write("", LOG_DEBUG, "All CTS handlers completed")

	// stop the main grpc server after all the other tasks are finished.
	s.gs.Stop()
}

func (s *Server) RunCtlTask(wg *sync.WaitGroup) {
	var err error

	defer wg.Done()

	err = s.ctl.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		fmt.Printf ("------------http server error - %s\n", err.Error())
	} else {
		fmt.Printf ("********* http server ended\n")
	}
}

func (s *Server) ReqStop() {
	if s.stop_req.CompareAndSwap(false, true) {
		var l *net.TCPListener
		var cts *ClientConn

		if (s.ctl != nil) {
			// shutdown the control server if ever started.
			s.ctl.Shutdown(s.ctx)
		}

		//s.gs.GracefulStop()
		//s.gs.Stop()
		for _, l = range s.l {
			l.Close()
		}

		s.cts_mtx.Lock() // TODO: this mya create dead-lock. check possibility of dead lock???
		for _, cts = range s.cts_map {
			cts.ReqStop() // request to stop connections from/to peer held in the cts structure
		}
		s.cts_mtx.Unlock()

		s.stop_chan <- true
		s.ctx_cancel()
	}
}

func (s *Server) AddNewClientConn(addr net.Addr, pss Hodu_PacketStreamServer) (*ClientConn, error) {
	var cts ClientConn
	var ok bool

	cts.svr = s
	cts.route_map = make(ServerRouteMap)
	cts.caddr = addr
	cts.pss = &GuardedPacketStreamServer{Hodu_PacketStreamServer: pss}

	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 8)

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	_, ok = s.cts_map[addr]
	if ok {
		return nil, fmt.Errorf("existing client - %s", addr.String())
	}
	s.cts_map[addr] = &cts
	s.log.Write("", LOG_DEBUG, "Added client connection from %s", addr.String())
	return &cts, nil
}

func (s *Server) RemoveClientConn(cts *ClientConn) {
	s.cts_mtx.Lock()
	delete(s.cts_map, cts.caddr)
	s.log.Write("", LOG_DEBUG, "Removed client connection from %s", cts.caddr.String())
	s.cts_mtx.Unlock()
}

func (s *Server) RemoveClientConnByAddr(addr net.Addr) {
	var cts *ClientConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map[addr]
	if ok {
		cts.ReqStop()
		delete(s.cts_map, cts.caddr)
	}
}

func (s *Server) FindClientConnByAddr (addr net.Addr) *ClientConn {
	var cts *ClientConn
	var ok bool

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	cts, ok = s.cts_map[addr]
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
	s.ext_svcs = append(s.ext_svcs, svc)
	s.wg.Add(1)
	go svc.RunTask(&s.wg)
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

func (s *Server) WriteLog (id string, level LogLevel, fmtstr string, args ...interface{}) {
	s.log.Write(id, level, fmtstr, args...)
}
