package hodu


//import "bufio"
import "context"
import "crypto/tls"
import "encoding/json"
import "errors"
import "fmt"
import "io"
import "net"
import "net/http"
import "sync"
import "sync/atomic"
import "time"

//import "github.com/google/uuid"
import "google.golang.org/grpc"
import "google.golang.org/grpc/credentials/insecure"

const PTC_LIMIT = 8192

type PacketStreamClient grpc.BidiStreamingClient[Packet, Packet]

type ServerConnMap = map[net.Addr]*ServerConn
type ClientPeerConnMap = map[uint32]*ClientPeerConn
type ClientRouteMap = map[uint32]*ClientRoute
type ClientPeerCancelFuncMap = map[uint32]context.CancelFunc

// --------------------------------------------------------------------
type ClientConfig struct {
	ServerAddr string
	PeerAddrs []string
}

type Client struct {
	ctx         context.Context
	ctx_cancel  context.CancelFunc
	tlscfg     *tls.Config

	ext_svcs   []Service
	ctl        *http.Server // control server

	cts_mtx     sync.Mutex
	cts_map     ServerConnMap

	wg          sync.WaitGroup
	stop_req    atomic.Bool
	stop_chan   chan bool

	log         Logger
}

type ClientPeerConn struct {
	route *ClientRoute
	conn_id uint32
	conn *net.TCPConn
	remot_conn_id uint32

	addr     string // peer address

	stop_chan chan bool
	stop_req atomic.Bool
	server_peer_eof atomic.Bool
}

// client connection to server
type ServerConn struct {
	cli      *Client
	cfg      *ClientConfig
	saddr    *net.TCPAddr // server address that is connected to

	conn     *grpc.ClientConn // grpc connection to the server
	hdc       HoduClient
	psc      *GuardedPacketStreamClient // guarded grpc stream

	s_seed    Seed
	c_seed    Seed

	route_mtx sync.Mutex
	route_map ClientRouteMap
	route_wg  sync.WaitGroup

	stop_req  atomic.Bool
	stop_chan chan bool
}

type ClientRoute struct {
	cts *ServerConn
	id uint32
	peer_addr *net.TCPAddr
	proto ROUTE_PROTO

	ptc_mtx        sync.Mutex
	ptc_map        ClientPeerConnMap
	ptc_cancel_map ClientPeerCancelFuncMap
	//ptc_limit      int
	//ptc_last_id    uint32
	ptc_wg sync.WaitGroup

	stop_req atomic.Bool
	stop_chan chan bool
}

type ClientCtlParamServer struct {
	ServerAddr string  `json:"server-addr"`
	PeerAddrs []string `json:"peer-addrs"`
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
func NewClientRoute(cts *ServerConn, id uint32, addr *net.TCPAddr, proto ROUTE_PROTO) *ClientRoute {
	var r ClientRoute

	r.cts = cts
	r.id = id
	//r.ptc_limit = PTC_LIMIT
	r.ptc_map = make(ClientPeerConnMap)
	r.ptc_cancel_map = make(ClientPeerCancelFuncMap)
	//r.ptc_last_id = 0
	r.proto = proto
	r.peer_addr = addr
	r.stop_req.Store(false)
	r.stop_chan = make(chan bool, 1)

	return &r
}

func (r *ClientRoute) RunTask(wg *sync.WaitGroup) {
	// this task on the route object isn't actually necessary.
	// most useful works are triggered by ReportEvent() and done by ConnectToPeer()
	defer wg.Done()

main_loop:
	for {
		select {
			case <- r.stop_chan:
				break main_loop
		}
	}

	r.ptc_wg.Wait() // wait for all peer tasks are finished
fmt.Printf ("*** End fo Client Roue Task\n")
}

func (r *ClientRoute) ReqStop() {
	if r.stop_req.CompareAndSwap(false, true) {
		var ptc *ClientPeerConn
		for _, ptc = range r.ptc_map {
			ptc.ReqStop()
		}

		r.stop_chan <- true
	}
fmt.Printf ("*** Sent stop request to Route..\n")
}

func (r* ClientRoute) ConnectToPeer(pts_id uint32, wg *sync.WaitGroup) {
	var err error
	var conn net.Conn
	var real_conn *net.TCPConn
	var ptc *ClientPeerConn
	var d net.Dialer
	var ctx context.Context
	var cancel context.CancelFunc
	var ok bool

	defer wg.Done()

// TODO: make timeuot value configurable
// TODO: fire the cancellation function upon stop request???
	ctx, cancel = context.WithTimeout(r.cts.cli.ctx, 10 * time.Second)
	r.ptc_mtx.Lock()
	r.ptc_cancel_map[pts_id] = cancel
	r.ptc_mtx.Unlock()

	d.LocalAddr = nil // TOOD: use this if local address is specified
	conn, err = d.DialContext(ctx, "tcp", r.peer_addr.String())

	r.ptc_mtx.Lock()
	delete(r.ptc_cancel_map, pts_id)
	r.ptc_mtx.Unlock()

	if err != nil {
// TODO: make send peer started failure mesage?
		fmt.Printf ("failed to connect to %s - %s\n", r.peer_addr.String(), err.Error())
		goto peer_aborted
	}

	real_conn, ok = conn.(*net.TCPConn)
	if !ok {
		fmt.Printf("not tcp connection - %s\n", err.Error())
		goto peer_aborted
	}

	ptc, err = r.AddNewClientPeerConn(real_conn, pts_id)
	if err != nil {
		// TODO: logging
// TODO: make send peer started failure mesage?
		fmt.Printf("YYYYYYYY - %s\n", err.Error())
		goto peer_aborted
	}
	fmt.Printf("STARTED NEW SERVER PEER STAK\n")
	err = r.cts.psc.Send(MakePeerStartedPacket(r.id, ptc.conn_id))
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

func (r* ClientRoute) DisconnectFromPeer(pts_id uint32) error {
	var ptc *ClientPeerConn
	var cancel context.CancelFunc
	var ok bool

	r.ptc_mtx.Lock()
	cancel, ok = r.ptc_cancel_map[pts_id]
	if ok {
fmt.Printf ("~~~~~~~~~~~~~~~~ cancelling.....\n")
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

func (r* ClientRoute) CloseWriteToPeer(pts_id uint32) error {
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


func (r* ClientRoute) ReportEvent (pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	var err error

	switch event_type {
		case PACKET_KIND_PEER_STARTED:
fmt.Printf ("GOT PEER STARTD . CONENCT TO CLIENT_SIDE PEER\n")
			r.ptc_wg.Add(1)
			go r.ConnectToPeer(pts_id, &r.ptc_wg)

		case PACKET_KIND_PEER_ABORTED:
			fallthrough
		case PACKET_KIND_PEER_STOPPED:
fmt.Printf ("GOT PEER STOPPED . DISCONNECTION FROM CLIENT_SIDE PEER\n")
			err = r.DisconnectFromPeer(pts_id)
			if err != nil {
				// TODO:
			}

		case PACKET_KIND_PEER_EOF:
fmt.Printf ("GOT PEER EOF. REMEMBER EOF\n")
			err = r.CloseWriteToPeer(pts_id)
			if err != nil {
				// TODO:
			}

		case PACKET_KIND_PEER_DATA:
			var ptc *ClientPeerConn
			var ok bool
			var err error
			ptc, ok = r.ptc_map[pts_id]
			if ok {
				_, err = ptc.conn.Write(event_data)
				return err
			} else {

			}

		// TODO: other types
	}

	return nil
}

// --------------------------------------------------------------------
func NewServerConn(c *Client, addr *net.TCPAddr, cfg *ClientConfig) *ServerConn {
	var cts ServerConn

	cts.cli = c
	cts.route_map = make(ClientRouteMap)
	cts.saddr = addr
	cts.cfg = cfg
	cts.stop_req.Store(false)
	cts.stop_chan = make(chan bool, 1)

	// the actual connection to the server is established in the main task function
	// The cts.conn, cts.hdc, cts.psc fields are left unassigned here.

	return &cts
}

func (cts *ServerConn) AddNewClientRoute(route_id uint32, addr *net.TCPAddr, proto ROUTE_PROTO) (*ClientRoute, error) {
	var r *ClientRoute

	cts.route_mtx.Lock()
	if cts.route_map[route_id] != nil {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf ("existent route id - %d", route_id)
	}
	r = NewClientRoute(cts, route_id, addr, proto)
	cts.route_map[route_id] = r
	cts.route_mtx.Unlock()

fmt.Printf ("added client route.... %d -> %d\n", route_id, len(cts.route_map))
	cts.route_wg.Add(1)
	go r.RunTask(&cts.route_wg)
	return r, nil
}

func (cts *ServerConn) RemoveClientRoute (route_id uint32) error {
	var r *ClientRoute
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
	return nil
}

func (cts *ServerConn) RemoveClientRoutes () {
	var r *ClientRoute
	var id uint32

	cts.route_mtx.Lock()
	for _, r = range cts.route_map {
		r.ReqStop()
	}

	for id, r = range cts.route_map {
		delete(cts.route_map, id)
	}

	cts.route_map = make(ClientRouteMap)
	cts.route_mtx.Unlock()
}

func (cts *ServerConn) AddClientRoutes (peer_addrs []string) error {
	var i int
	var v string
	var addr *net.TCPAddr
	var proto ROUTE_PROTO
	var r *ClientRoute
	var err error

	for i, v = range peer_addrs {
		addr, err = net.ResolveTCPAddr(NET_TYPE_TCP, v) // Make this interruptable
		if err != nil {
			return fmt.Errorf("unable to resovle %s - %s", v, err.Error())
		}

		if addr.IP.To4() != nil {
			proto = ROUTE_PROTO_TCP4
		} else {
			proto = ROUTE_PROTO_TCP6
		}

		_, err = cts.AddNewClientRoute(uint32(i), addr, proto)
		if err != nil {
			return fmt.Errorf("unable to add client route for %s - %s", addr, err.Error())
		}
	}

	for _, r = range cts.route_map  {
		err = cts.psc.Send(MakeRouteStartPacket(r.id, r.proto, addr.String()))
		if err != nil {
			return fmt.Errorf("unable to send route-start packet - %s", err.Error())
		}
	}

	return nil
}

func (cts *ServerConn) ReqStop() {
	if cts.stop_req.CompareAndSwap(false, true) {
		var r *ClientRoute

		cts.route_mtx.Lock()
		for _, r = range cts.route_map {
			r.ReqStop()
		}
		cts.route_mtx.Unlock()

		// TODO: notify the server.. send term command???
		cts.stop_chan <- true
	}
fmt.Printf ("*** Sent stop request to ServerConn..\n")
}

func (cts *ServerConn) RunTask(wg *sync.WaitGroup) {
	var conn *grpc.ClientConn = nil
	var hdc HoduClient
	var psc PacketStreamClient
	var slpctx context.Context
	var c_seed Seed
	var s_seed *Seed
	var err error

	defer wg.Done() // arrange to call at the end of this function

// TODO: HANDLE connection timeout..
	//	ctx, _/*cancel*/ := context.WithTimeout(context.Background(), time.Second)
start_over:
fmt.Printf ("Connecting GRPC to [%s]\n", cts.saddr.String())
	conn, err = grpc.NewClient(cts.saddr.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
	// TODO: logging
		fmt.Printf("ERROR: unable to make grpc client to %s - %s\n", cts.cfg.ServerAddr, err.Error())
		goto reconnect_to_server
	}

	hdc = NewHoduClient(conn)

	// seed exchange is for furture expansion of the protocol
	// there is nothing to do much about it for now.
	c_seed.Version = HODU_VERSION
	c_seed.Flags = 0
	s_seed, err = hdc.GetSeed(cts.cli.ctx, &c_seed)
	if err != nil {
		fmt.Printf("ERROR: unable to get seed from %s - %s\n", cts.cfg.ServerAddr, err.Error())
		goto reconnect_to_server
	}
	cts.s_seed = *s_seed
	cts.c_seed = c_seed

	psc, err = hdc.PacketStream(cts.cli.ctx)
	if err != nil {
		fmt.Printf ("ERROR: unable to get grpc packet stream - %s\n", err.Error())
		goto reconnect_to_server
	}

	cts.conn = conn
	cts.hdc = hdc
	//cts.psc = &GuardedPacketStreamClient{psc: psc}
	cts.psc = &GuardedPacketStreamClient{Hodu_PacketStreamClient: psc}

	// the connection structure to a server is ready.
	// let's add routes to the client-side peers.
	err = cts.AddClientRoutes(cts.cfg.PeerAddrs)
	if err != nil {
		fmt.Printf ("ERROR: unable to add routes to client-side peers - %s\n", err.Error())
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
		if errors.Is(err, io.EOF) {
			fmt.Printf("server disconnected\n")
			goto reconnect_to_server
		}
		if err != nil {
			fmt.Printf("server receive error - %s\n", err.Error())
			goto reconnect_to_server
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_STARTED:
				// the server side managed to set up the route the client requested
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
	fmt.Printf ("SERVER LISTENING ON %s\n", x.Route.AddrStr)
					err = cts.ReportEvent(x.Route.RouteId, 0, pkt.Kind, nil)
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
					err = cts.ReportEvent(x.Peer.RouteId, x.Peer.PeerId, PACKET_KIND_PEER_STARTED, nil)
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
	fmt.Printf ("**** GOT PEER DATA\n")
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(x.Data.RouteId, x.Data.PeerId, PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
		fmt.Printf ("failed to report event - %s\n", err.Error())
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
fmt.Printf ("^^^^^^^^^^^^^^^^^^^^ Server Coon RunTask ending...\n")
	if conn != nil {
		conn.Close()
		// TODO: need to reset c.sc, c.sg, c.psc to nil?
		//       for this we need to ensure that everyone is ending
	}
	cts.RemoveClientRoutes()
	cts.route_wg.Wait() // wait until all route tasks are finished
	return

reconnect_to_server:
	if conn != nil {
		conn.Close()
		// TODO: need to reset c.sc, c.sg, c.psc to nil?
		//       for this we need to ensure that everyone is ending
	}
	cts.RemoveClientRoutes()
	slpctx, _ = context.WithTimeout(cts.cli.ctx, 3 * time.Second)
	select {
		case <-cts.cli.ctx.Done():
			fmt.Printf("context doine... error - %s\n", cts.cli.ctx.Err().Error())
			goto done
		case <-cts.stop_chan:
			goto done
		case <- slpctx.Done():
			// do nothing
	}
	goto start_over
}

func (cts *ServerConn) ReportEvent (route_id uint32, pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	var r *ClientRoute
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
// --------------------------------------------------------------------

func (r *ClientRoute) AddNewClientPeerConn (c *net.TCPConn, pts_id uint32) (*ClientPeerConn, error) {
	var ptc *ClientPeerConn
	//var ok bool
	//var start_id uint32

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

/*
	if len(r.ptc_map) >= r.ptc_limit {
		return nil, fmt.Errorf("peer-to-client connection table full")
	}

	start_id = r.ptc_last_id
	for {
		_, ok = r.ptc_map[r.ptc_last_id]
		if !ok {
			break
		}
		r.ptc_last_id++
		if r.ptc_last_id == start_id {
			// unlikely to happen but it cycled through the whole range.
			return nil, fmt.Errorf("failed to assign peer-to-table connection id")
		}
	}

	ptc = NewClientPeerConn(r, c, r.ptc_last_id)
*/
	ptc = NewClientPeerConn(r, c, pts_id)
	r.ptc_map[ptc.conn_id] = ptc
	//r.ptc_last_id++

	return ptc, nil
}

// --------------------------------------------------------------------


func NewClient(ctx context.Context, listen_on string, logger Logger, tlscfg *tls.Config) *Client {
	var c Client

	c.ctx, c.ctx_cancel = context.WithCancel(ctx)
	c.tlscfg = tlscfg
	c.ext_svcs = make([]Service, 0, 1)
	c.cts_map = make(ServerConnMap) // TODO: make it configurable...
	c.stop_req.Store(false)
	c.stop_chan = make(chan bool, 1)
	c.log = logger

	c.ctl = &http.Server{
		Addr: listen_on,
		Handler: &c,
	}

	return &c
}

func (c *Client) AddNewServerConn(addr *net.TCPAddr, cfg *ClientConfig) (*ServerConn, error) {
	var cts *ServerConn
	var ok bool

	cts = NewServerConn(c, addr, cfg)

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	_, ok = c.cts_map[addr]
	if ok {
		return nil, fmt.Errorf("existing server - %s", addr.String())
	}

	c.cts_map[addr] = cts
fmt.Printf ("ADD total servers %d\n", len(c.cts_map))
	return cts, nil
}

func (c *Client) RemoveServerConn(cts *ServerConn) {
	c.cts_mtx.Lock()
	delete(c.cts_map, cts.saddr)
fmt.Printf ("REMOVE total servers %d\n", len(c.cts_map))
	c.cts_mtx.Unlock()
}

func (c *Client) ReqStop() {
	if c.stop_req.CompareAndSwap(false, true) {
		var cts *ServerConn

		c.ctl.Shutdown(c.ctx) // to break c.ctl.ListenAndServe()

		for _, cts = range c.cts_map {
			cts.ReqStop()
		}

		// TODO: notify the server.. send term command???
		c.stop_chan <- true
		c.ctx_cancel()
	}
fmt.Printf ("*** Sent stop request to client..\n")
}

func (c *Client) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var err error

	// command handler for the control channel
	if req.URL.String() == "/servers" {
		switch req.Method {
			case http.MethodGet:
				goto bad_request // TODO:

			case http.MethodPost:
				var s ClientCtlParamServer
				var cc ClientConfig
				err = json.NewDecoder(req.Body).Decode(&s)
				if err != nil {
		fmt.Printf ("failed to decode body - %s\n", err.Error())
					goto bad_request
				}
				cc.ServerAddr = s.ServerAddr
				cc.PeerAddrs = s.PeerAddrs
				c.StartService(&cc) // TODO: this can be blocking. do we have to resolve addresses before calling this? also not good because resolution succeed or fail at each attempt.  however ok as ServeHTTP itself is in a goroutine?
				w.WriteHeader(http.StatusCreated)

			case http.MethodPut:
				goto bad_request // TODO:

			case http.MethodDelete:
				var cts *ServerConn
				for _, cts = range c.cts_map {
					cts.ReqStop()
				}
		}
	} else {
		goto bad_request
	}
	fmt.Printf ("[%s][%s][%s]\n", req.RequestURI, req.URL.String(), req.Method)
	return

bad_request:
	w.WriteHeader(http.StatusBadRequest)
	return
}

/*
 *                       POST                 GET            PUT            DELETE
 * /servers -     create new server     list all servers    bulk update     delete all servers
 * /servers/1 -        X             get server 1 details  update server 1  delete server 1
 * /servers/1/xxx -
 */
func (c *Client) RunCtlTask(wg *sync.WaitGroup) {
	var err error

	defer wg.Done()

	err = c.ctl.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		fmt.Printf ("------------http server error - %s\n", err.Error())
	} else {
		fmt.Printf ("********* http server ended\n")
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

// naming convention:
//   RunService - returns after having executed another go routine
//   RunTask - supposed to be detached as a go routine
func (c *Client) StartService(data interface{}) {
	var saddr *net.TCPAddr
	var cts *ServerConn
	var err error
	var cfg *ClientConfig
	var ok bool

	cfg, ok = data.(*ClientConfig)
	if !ok {
		fmt.Printf("invalid configuration given")
		return
	}

	if len(cfg.PeerAddrs) < 0 || len(cfg.PeerAddrs) > int(^uint16(0)) { // TODO: change this check... not really right...
		fmt.Printf("no peer addresses or too many peer addresses")
		return
	}

	saddr, err = net.ResolveTCPAddr(NET_TYPE_TCP, cfg.ServerAddr) // TODO: make this interruptable...
	if err != nil {
		fmt.Printf("unable to resolve %s - %s", cfg.ServerAddr, err.Error())
		return
	}

	cts, err = c.AddNewServerConn(saddr, cfg)
	if err != nil {
		fmt.Printf("unable to add server connection structure to %s - %s", cfg.ServerAddr, err.Error())
		return
	}

	c.wg.Add(1)
	go cts.RunTask(&c.wg)
}

func (c *Client) StartExtService(svc Service, data interface{}) {
	c.ext_svcs = append(c.ext_svcs, svc)
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

func (c *Client) WriteLog (id string, level LogLevel, fmtstr string, args ...interface{}) {
	c.log.Write(id, level, fmtstr, args...)
}
