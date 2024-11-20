package main

import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "io"
import "log"
import "math/rand"
import "net"
import "net/http"
import "os"
import "os/signal"
import "sync"
import "sync/atomic"
import "syscall"
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
	tlscfg      *tls.Config
	wg          sync.WaitGroup
	stop_req    atomic.Bool

	ctl        *http.Server // control server

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

	l, laddr, err = cts.make_route_listener(proto);
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

	return &r, nil;
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
	r.cts.svr.log.Write(LOG_DEBUG, "Removed server-side peer connection %s", pts.conn.RemoteAddr().String())
}

func (r *ServerRoute) RunTask(wg *sync.WaitGroup) {
	var err error
	var conn *net.TCPConn
	var pts *ServerPeerConn

	defer wg.Done()

	for {
		conn, err = r.l.AcceptTCP()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				r.cts.svr.log.Write(LOG_INFO, "[%s,%d] Service-side peer listener closed\n", r.cts.caddr.String(), r.id)
			} else {
				fmt.Printf("[%s,%d] Server-side peer listener error - %s\n", r.cts.caddr.String(), r.id, err.Error())
			}
			break
		}

		pts, err = r.AddNewServerPeerConn(conn)
		if err != nil {
			r.cts.svr.log.Write(LOG_ERROR, "[%s,%d] Failed to add new server-side peer %s - %s", r.cts.caddr.String(), r.id, conn.RemoteAddr().String(), err.Error())
			conn.Close()
		} else {
			r.cts.svr.log.Write(LOG_DEBUG, "[%s,%d] Added new server-side peer %s", r.cts.caddr.String(), r.id, conn.RemoteAddr().String())
			r.pts_wg.Add(1)
			go pts.RunTask(&r.pts_wg)
		}
	}

	r.l.Close() // don't care about double close. it could have been closed in ReqStop
	r.pts_wg.Wait()
	r.cts.svr.log.Write(LOG_DEBUG, "[%s,%d] All service-side peer handlers completed", r.cts.caddr.String(), r.id)
}

func (r *ServerRoute) ReqStop() {
	fmt.Printf ("requesting to stop route taak..\n")

	if r.stop_req.CompareAndSwap(false, true) {
		var pts *ServerPeerConn

		for _, pts = range r.pts_map {
			pts.ReqStop()
		}

		r.l.Close();
	}
	fmt.Printf ("requiested to stopp route taak..\n")
}

func (r *ServerRoute) ReportEvent (pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	var spc *ServerPeerConn
	var ok bool

	r.pts_mtx.Lock()
	spc, ok = r.pts_map[pts_id]
	if !ok {
		r.pts_mtx.Unlock();
		return fmt.Errorf("non-existent peer id - %u", pts_id)
	}
	r.pts_mtx.Unlock();

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
	cts.route_map[route_id] = r;
	cts.route_mtx.Unlock()

	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ClientConn) RemoveServerRoute (route_id uint32) error {
	var r *ServerRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.route_map[route_id]
	if (!ok) {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route id - %d", route_id)
	}
	delete(cts.route_map, route_id)
	cts.route_mtx.Unlock()

	r.ReqStop() // TODO: make this unblocking or blocking?
	return nil;
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
			// return will close stream from server side
// TODO: clean up route_map and server-side peers releated to the client connection 'cts'
fmt.Printf ("grpd stream ended\n")
			goto done
		}
		if err != nil {
			//log.Printf("receive error %v", err)
			fmt.Printf ("grpc stream error - %s\n", err.Error())
			goto done
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_START:
				var x *Packet_Route
				//var t *ServerRoute
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					var r* ServerRoute
		fmt.Printf ("ADDED SERVER ROUTE FOR CLEINT PEER %s\n", x.Route.AddrStr)
					r, err = cts.AddNewServerRoute(x.Route.RouteId, x.Route.Proto)
					if err != nil {
						// TODO: Send Error Response...
					} else {
						err = cts.pss.Send(MakeRouteStartedPacket(r.id, x.Route.Proto, r.laddr.String()))
						if err != nil {
							// TODO:
						}
					}
				} else {
					// TODO: send invalid request... or simply keep quiet?
				}

			case PACKET_KIND_ROUTE_STOP:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
					err = cts.RemoveServerRoute(x.Route.RouteId); // TODO: this must be unblocking. otherwide, other route_map will get blocked...
					if err != nil {
						// TODO: Send Error Response...
					} else {
						err = cts.pss.Send(MakeRouteStoppedPacket(x.Route.RouteId, x.Route.Proto))
						if err != nil {
							// TODO:
						}
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
fmt.Printf("grpd server done - %s\n", ctx.Err().Error())
				goto done

			case <- cts.stop_chan:
				goto done

			//default:
				// no other case is ready.
				// without the default case, the select construct would block
		}
	}

done:
fmt.Printf ("^^^^^^^^^^^^^^^^^ waiting for reoute_wg...\n")
	cts.route_wg.Wait()
fmt.Printf ("^^^^^^^^^^^^^^^^^ waited for reoute_wg...\n")
}

func (cts *ClientConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		var r *ServerRoute

		for _, r = range cts.route_map {
			r.ReqStop()
		}

		cts.stop_chan <- true
		//cts.c.Close() // close the accepted connection from the client
	}
}

// ------------------------------------

func (s *Server) handle_os_signals() {
	var sighup_chan  chan os.Signal
	var sigterm_chan chan os.Signal
	var sig          os.Signal

	defer s.wg.Done()

	sighup_chan = make(chan os.Signal, 1)
	sigterm_chan = make(chan os.Signal, 1)

	signal.Notify(sighup_chan, syscall.SIGHUP)
	signal.Notify(sigterm_chan, syscall.SIGTERM, os.Interrupt)

chan_loop:
	for {
		select {
		case <-sighup_chan:
			// TODO:
			//s.RefreshConfig()
		case sig = <-sigterm_chan:
			// TODO: get timeout value from config
			//s.Shutdown(fmt.Sprintf("termination by signal %s", sig), 3*time.Second)
			s.ReqStop()
			//log.Debugf("termination by signal %s", sig)
			fmt.Printf("termination by signal %s\n", sig)
			break chan_loop
		}
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
	return ctx;
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
			cc.server.RemoveClientConnByAddr(p.Addr);
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

func NewServer(laddrs []string, logger Logger, tlscfg *tls.Config) (*Server, error) {
	var s Server
	var l *net.TCPListener
	var laddr *net.TCPAddr
	var err error
	var addr string
	var gl *net.TCPListener

	if len(laddrs) <= 0 {
		return nil, fmt.Errorf("no or too many addresses provided")
	}

	s.log = logger
	/* create the specified number of listeners */
	s.l = make([]*net.TCPListener, 0)
	for _, addr = range laddrs {
		laddr, err = net.ResolveTCPAddr(NET_TYPE_TCP, addr)
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
	s.cts_map = make(ClientConnMap) // TODO: make it configurable...
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

	defer wg.Done();

	l = s.l[idx]
	// it seems to be safe to call a single grpc server on differnt listening sockets multiple times
	s.log.Write (LOG_ERROR, "Starting GRPC server listening on %s", l.Addr().String())
	err = s.gs.Serve(l);
	if err != nil {
		if errors.Is(err, net.ErrClosed) {
			s.log.Write (LOG_ERROR, "GRPC server listening on %s closed", l.Addr().String())
		} else {
			s.log.Write (LOG_ERROR, "Error from GRPC server listening on %s - %s", l.Addr().String(), err.Error())
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

	s.l_wg.Wait()
	s.log.Write(LOG_DEBUG, "All GRPC listeners completed")
	s.cts_wg.Wait()
	s.log.Write(LOG_DEBUG, "All CTS handlers completed")

	s.ReqStop()

	// stop the main grpc server after all the other tasks are finished.
	s.gs.Stop()

	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
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
	cts.stop_chan = make(chan bool, 1)

	s.cts_mtx.Lock()
	defer s.cts_mtx.Unlock()

	_, ok = s.cts_map[addr]
	if ok {
		return nil, fmt.Errorf("existing client - %s", addr.String())
	}
	s.cts_map[addr] = &cts;
	s.log.Write(LOG_DEBUG, "Added client connection from %s", addr.String())
	return &cts, nil
}

func (s *Server) RemoveClientConn(cts *ClientConn) {
	s.cts_mtx.Lock()
	delete(s.cts_map, cts.caddr)
	s.log.Write(LOG_DEBUG, "Removed client connection from %s", cts.caddr.String())
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

// --------------------------------------------------------------------

const serverKey = `-----BEGIN EC PARAMETERS-----
BggqhkjOPQMBBw==
-----END EC PARAMETERS-----
-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIHg+g2unjA5BkDtXSN9ShN7kbPlbCcqcYdDu+QeV8XWuoAoGCCqGSM49
AwEHoUQDQgAEcZpodWh3SEs5Hh3rrEiu1LZOYSaNIWO34MgRxvqwz1FMpLxNlx0G
cSqrxhPubawptX5MSr02ft32kfOlYbaF5Q==
-----END EC PRIVATE KEY-----
`

const serverCert = `-----BEGIN CERTIFICATE-----
MIIB+TCCAZ+gAwIBAgIJAL05LKXo6PrrMAoGCCqGSM49BAMCMFkxCzAJBgNVBAYT
AkFVMRMwEQYDVQQIDApTb21lLVN0YXRlMSEwHwYDVQQKDBhJbnRlcm5ldCBXaWRn
aXRzIFB0eSBMdGQxEjAQBgNVBAMMCWxvY2FsaG9zdDAeFw0xNTEyMDgxNDAxMTNa
Fw0yNTEyMDUxNDAxMTNaMFkxCzAJBgNVBAYTAkFVMRMwEQYDVQQIDApTb21lLVN0
YXRlMSEwHwYDVQQKDBhJbnRlcm5ldCBXaWRnaXRzIFB0eSBMdGQxEjAQBgNVBAMM
CWxvY2FsaG9zdDBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABHGaaHVod0hLOR4d
66xIrtS2TmEmjSFjt+DIEcb6sM9RTKS8TZcdBnEqq8YT7m2sKbV+TEq9Nn7d9pHz
pWG2heWjUDBOMB0GA1UdDgQWBBR0fqrecDJ44D/fiYJiOeBzfoqEijAfBgNVHSME
GDAWgBR0fqrecDJ44D/fiYJiOeBzfoqEijAMBgNVHRMEBTADAQH/MAoGCCqGSM49
BAMCA0gAMEUCIEKzVMF3JqjQjuM2rX7Rx8hancI5KJhwfeKu1xbyR7XaAiEA2UT7
1xOP035EcraRmWPe7tO0LpXgMxlh2VItpc2uc2w=
-----END CERTIFICATE-----
`

type serverLogger struct {
	log *log.Logger
}


func (log* serverLogger) Write(level LogLevel, fmt string, args ...interface{}) {
	log.log.Printf(fmt, args...)
}

func server_main(laddrs []string) error {
	var s *Server
	var err error

	var sl serverLogger
	var cert tls.Certificate

	cert, err = tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
	if err != nil {
		return fmt.Errorf("ERROR: failed to load key pair - %s\n", err)
	}

	sl.log = log.Default()
	s, err = NewServer(laddrs, &sl, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return fmt.Errorf("ERROR: failed to create new server - %s", err.Error())
	}

	s.wg.Add(1)
	go s.handle_os_signals()

	s.wg.Add(1)
	go s.RunTask(&s.wg) // this is blocking. ReqStop() will be called from a signal handler
	s.wg.Wait()

	return nil
}
