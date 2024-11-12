package main


//import "bufio"
import "context"
import "crypto/tls"
import "crypto/x509"
//import "encoding/binary"
import "fmt"
import "io"
import "log"
import "net"
import "os"
import "os/signal"
import "sync"
import "sync/atomic"
import "syscall"
//import "time"

//import "github.com/google/uuid"
import "google.golang.org/grpc"
import "google.golang.org/grpc/credentials/insecure"

const PTC_LIMIT = 8192

type PacketStreamClient grpc.BidiStreamingClient[Packet, Packet]

type ServerConnMap = map[*net.TCPAddr]*ServerConn
type ClientPeerConnMap = map[uint32]*ClientPeerConn
type ClientRouteMap = map[uint32]*ClientRoute

// --------------------------------------------------------------------
type ClientConfig struct {
	server_addr string
	peer_addrs []string
}

type Client struct {
	cfg     *ClientConfig
	tlscfg  *tls.Config
	saddr   *net.TCPAddr

	sc     *grpc.ClientConn // main control connection to the server 
	sg      HoduClient
	psc      PacketStreamClient
	psc_mtx sync.Mutex

	cts_mtx     sync.Mutex
	cts_map     ServerConnMap
	wg       sync.WaitGroup
	stop_req atomic.Bool
}


type ClientPeerConn struct {
	route *ClientRoute
	conn_id uint32
	conn *net.TCPConn
	remot_conn_id uint32

	addr     string // peer address
	stop_req atomic.Bool
}

// client connection to server
type ServerConn struct {
	cli    *Client
	saddr  *net.TCPAddr // server address that is connected to
	psc    Hodu_PacketStreamClient

	route_mtx  sync.Mutex
	routes ClientRouteMap
	//route_wg   sync.WaitGroup

	//cw_mtx sync.Mutex

	wg       sync.WaitGroup
	stop_req atomic.Bool
	greeted  bool
}

type ClientRoute struct {
	cts *ServerConn
	id uint32
	peer_addr *net.TCPAddr
	proto ROUTE_PROTO

	ptc_mtx     sync.Mutex
	ptc_map     ClientPeerConnMap
	ptc_limit   int
	ptc_last_id uint32
	ptc_wg sync.WaitGroup
}


// --------------------------------------------------------------------
func NewClientRoute(cts *ServerConn, id uint32, addr *net.TCPAddr, proto ROUTE_PROTO) *ClientRoute {
	var r ClientRoute

	r.cts = cts
	r.id = id
	r.ptc_limit = PTC_LIMIT
	r.ptc_map = make(ClientPeerConnMap)
	r.ptc_last_id = 0
	r.proto = proto
	r.peer_addr = addr

	return &r;
}

func (r *ClientRoute) RunTask() {
	// this task on the route object isn't actually necessary.
}

func (r *ClientRoute) StopTask() {
	// TODO:
	fmt.Printf ("ClientRoute StopTask not implemented yet\n")
	// TOOD: stop all peer connection jobs
}

func (r* ClientRoute) ConnectToPeer(pts_id uint32) {
	var err error
	var conn *net.TCPConn
	var ptc *ClientPeerConn

// MAKE thesse into a separte go rountine... so it doesn't block
	conn, err = net.DialTCP("tcp", nil, r.peer_addr);
	if err != nil {
		fmt.Printf ("failed to connect to %s - %s\n", r.peer_addr.String(), err.Error())
		return
	}

	ptc, err = r.AddNewClientPeerConn(conn)
	if err != nil {
		// TODO: logging
		fmt.Printf("YYYYYYYY - %s\n", err.Error())
		conn.Close()
		return
	}
	fmt.Printf("STARTED NEW SERVER PEER STAK\n")

	r.ptc_wg.Add(1)
	go ptc.RunTask()
	r.ptc_wg.Wait()
	conn.Close() // don't care about double close. it could have been closed in StopTask
}

func (r* ClientRoute) ReportEvent (pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	switch event_type {
		case PACKET_KIND_PEER_STARTED:
			go r.ConnectToPeer(pts_id)

		// TODO: other types
	}

	return nil
}

// --------------------------------------------------------------------

func (cts *ServerConn) AddNewClientRoute(route_id uint32, addr *net.TCPAddr, proto ROUTE_PROTO) (*ClientRoute, error) {
	var r *ClientRoute

	cts.route_mtx.Lock()
	if cts.routes[route_id] != nil {
		cts.route_mtx.Unlock()
		return nil, fmt.Errorf ("existent route id - %d", route_id)
	}
	r = NewClientRoute(cts, route_id, addr, proto)
	cts.routes[route_id] = r
	cts.route_mtx.Unlock()

fmt.Printf ("added client route.... %d -> %d\n", route_id, len(cts.routes))
	go r.RunTask()
	return r, nil
}

func (cts *ServerConn) RemoveClientRoute (route_id uint32) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.routes[route_id]
	if (!ok) {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route id - %d", route_id)
	}
	delete(cts.routes, route_id)
	cts.route_mtx.Unlock()

	r.StopTask() // TODO: make this unblocking or blocking?
	return nil;
}

func (cts *ServerConn) AddClientRoutes (peer_addrs []string) error {
	var i int
	var v string
	var addr *net.TCPAddr
	var proto ROUTE_PROTO
	var r *ClientRoute
	var err error

	for i, v = range peer_addrs {
		addr, err = net.ResolveTCPAddr(NET_TYPE_TCP, v)
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
			return fmt.Errorf("unable to add client route for %s", addr)
		}
	}

	for _, r = range cts.routes  {
		err = cts.cli.psc.Send(MakeRouteStartPacket(r.id, r.proto, addr.String()))
		if err != nil {
			return fmt.Errorf("unable to send route-start packet - %s", err.Error())
		}
	}

	return nil;
}

func (cts *ServerConn) ReportEvent (route_id uint32, pts_id uint32, event_type PACKET_KIND, event_data []byte) error {
	var r *ClientRoute
	var ok bool

	cts.route_mtx.Lock()
	r, ok = cts.routes[route_id]
	if (!ok) {
		cts.route_mtx.Unlock()
		return fmt.Errorf ("non-existent route id - %d", route_id)
	}
	cts.route_mtx.Unlock()
	
	return r.ReportEvent(pts_id, event_type, event_data)
}
// --------------------------------------------------------------------

func (r *ClientRoute) AddNewClientPeerConn (c* net.TCPConn) (*ClientPeerConn, error) {
	var ptc *ClientPeerConn
	var ok bool
	var start_id uint32

	r.ptc_mtx.Lock()
	defer r.ptc_mtx.Unlock()

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
	r.ptc_map[ptc.conn_id] = ptc
	r.ptc_last_id++

	return ptc, nil
}
// --------------------------------------------------------------------

func (c *Client) AddNewServerConn(addr *net.TCPAddr, psc Hodu_PacketStreamClient) (*ServerConn, error) {
	var cts ServerConn
	var ok bool

	cts.cli = c
	cts.routes = make(ClientRouteMap)
	cts.saddr = addr 
	cts.psc = psc

	cts.stop_req.Store(false)
	cts.greeted = false

	c.cts_mtx.Lock()
	defer c.cts_mtx.Unlock()

	_, ok = c.cts_map[addr]
	if ok {
		return nil, fmt.Errorf("existing server - %s", addr.String())
	}

	c.cts_map[addr] = &cts;
fmt.Printf ("ADD total servers %d\n", len(c.cts_map));
	return &cts, nil
}

func (c *Client) RemoveServerConn(cts *ServerConn) {
	c.cts_mtx.Lock()
	delete(c.cts_map, cts.saddr)
fmt.Printf ("REMOVE total servers %d\n", len(c.cts_map));
	c.cts_mtx.Unlock()
}

// --------------------------------------------------------------------
func NewClient(cfg *ClientConfig, tlscfg *tls.Config) (*Client, error) {
	var c Client
	var saddr *net.TCPAddr
	var err error

	if len(cfg.peer_addrs) < 0 || len(cfg.peer_addrs) > int(^uint16(0)) { // TODO: change this check... not really right...
		return nil, fmt.Errorf("no peer addresses or too many peer addresses")
	}

	saddr, err = net.ResolveTCPAddr(NET_TYPE_TCP, cfg.server_addr)
	if err != nil {
		return nil, fmt.Errorf("unable to resolve %s - %s", cfg.server_addr, err.Error())
	}

	c.cfg = cfg
	c.tlscfg = tlscfg
	c.saddr = saddr
	c.cts_map = make(ServerConnMap) // TODO: make it configurable...
	c.stop_req.Store(false)

	return &c, nil
}

func (c *Client) RunTask(ctx context.Context) {
	var conn *grpc.ClientConn
	var cts *ServerConn
	var err error

	defer c.wg.Done();

// TODO: HANDLE connection timeout..
	//	ctx, _/*cancel*/ := context.WithTimeout(context.Background(), time.Second)
	conn, err = grpc.NewClient(c.saddr.String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
	// TODO: logging
		fmt.Printf("ERROR - unable to connect to %s - %s", c.cfg.server_addr, err.Error())
		return
	}

	c.sc = conn
	c.sg = NewHoduClient(conn)

	c.psc, err = c.sg.PacketStream(ctx) // TODO: accept external context and use it.L
	if err != nil {
		conn.Close()
		fmt.Printf ("failed to get the packet stream - %s", err.Error())
		return
	}

	cts, err = c.AddNewServerConn(c.saddr, c.psc)
	if err != nil {
		conn.Close()
		fmt.Printf ("failed to register connection to server - %s", err.Error())
		return
	}

	err = cts.AddClientRoutes(c.cfg.peer_addrs)
	if err != nil {
		conn.Close()
		fmt.Printf("unable to make client routes - %s", err.Error())
		return
	}

	for {
		var pkt *Packet

		select {
			case <-ctx.Done():
				fmt.Printf("context doine... error - %s\n", ctx.Err().Error())
			default:
				// no other case is ready.
				// without the default case, the select construct would block
		}

		pkt, err = c.psc.Recv()
		if err == io.EOF {
			// return will close stream from server side
			fmt.Printf("server disconnected\n")
			break
		}
		if err != nil {
			fmt.Printf("server receive error - %s\n", err.Error())
			break
		}

		switch pkt.Kind {
			case PACKET_KIND_ROUTE_STARTED:
				var x *Packet_Route
				var ok bool
				x, ok = pkt.U.(*Packet_Route)
				if ok {
	fmt.Printf ("SERVER LISTENING ON %s\n", x.Route.AddrStr);
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

			case PACKET_KIND_PEER_DATA:
				// the connection from the client to a peer has been established
				var x *Packet_Data
				var ok bool
				x, ok = pkt.U.(*Packet_Data)
				if ok {
					err = cts.ReportEvent(x.Data.RouteId, x.Data.PeerId, PACKET_KIND_PEER_DATA, x.Data.Data)
					if err != nil {
						// TODO:
					} else {
						// TODO: 
					}
				} else {
					// TODO
				}
		}
	}

//done:
	c.ReqStop() // just in case...
	c.sc.Close()

	syscall.Kill(syscall.Getpid(), syscall.SIGTERM) // TODO: find a better to terminate the signal handler...
}

func (c *Client) ReqStop() {
	if c.stop_req.CompareAndSwap(false, true) {
		// TODO: notify the server.. send term command???
		c.sc.Close()
	}
}


// --------------------------------------------------------------------

func (c *Client) handle_os_signals() {
	var sighup_chan  chan os.Signal
	var sigterm_chan chan os.Signal
	var sig          os.Signal

	defer c.wg.Done()

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
			//c.Shutdown(fmt.Sprintf("termination by signal %s", sig), 3*time.Second)
			c.ReqStop()
			//log.Debugf("termination by signal %s", sig)
			fmt.Printf("termination by signal %s\n", sig)
			break chan_loop
		}
	}

fmt.Printf ("end of signal handler...\n");
}

// --------------------------------------------------------------------

const rootCert = `-----BEGIN CERTIFICATE-----
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

func client_main(server_addr string, peer_addrs []string) error {
	var c *Client
	var err error
	var cert_pool *x509.CertPool
	var tlscfg *tls.Config
	var cc ClientConfig

	cert_pool = x509.NewCertPool()
	ok := cert_pool.AppendCertsFromPEM([]byte(rootCert))
	if !ok {
		log.Fatal("failed to parse root certificate")
	}
	tlscfg = &tls.Config{RootCAs: cert_pool, ServerName: "localhost", InsecureSkipVerify: true}

	cc.server_addr = server_addr
	cc.peer_addrs = peer_addrs
	c, err = NewClient(&cc, tlscfg)
	if err != nil {
		fmt.Printf("failed create client - %s\n", err.Error())
		return err
	}

fmt.Printf ("XXXXXXXXXXXXXXXXXXXXXXXXXXXX\n");
	c.wg.Add(1)
	go c.handle_os_signals()
	c.wg.Add(1)
	go c.RunTask(context.Background());
	c.wg.Wait();
fmt.Printf ("YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY\n");

	return nil
}
