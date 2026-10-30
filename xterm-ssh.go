package hodu

import "context"
import "io"
import "net"
import "sync"
import "time"

import "golang.org/x/crypto/ssh"

type xterm_ssh_credentials struct {
	username string
	password string
}

type xterm_ssh_pool struct {
	mtx sync.Mutex
	clients map[xterm_ssh_credentials]*ssh.Client
	leases map[*ssh.Client]int
	route *ServerRoute
	count func(int64)
	wg sync.WaitGroup
}

func (pool *xterm_ssh_pool) close() {
	var client *ssh.Client

	pool.mtx.Lock()
	for _, client = range pool.clients { client.Close() }
	pool.mtx.Unlock()
	pool.wg.Wait()
}

func (pool *xterm_ssh_pool) get_client(ctx context.Context, credentials xterm_ssh_credentials) (*ssh.Client, error) {
	var client *ssh.Client
	var addr *net.TCPAddr
	var dialer net.Dialer
	var socket net.Conn
	var transport ssh.Conn
	var channels <-chan ssh.NewChannel
	var requests <-chan *ssh.Request
	var stop_close func() bool
	var err error

	pool.mtx.Lock()
	defer pool.mtx.Unlock()
	if ctx.Err() != nil { return nil, ctx.Err() }
	client = pool.clients[credentials]
	if client != nil { pool.leases[client]++; return client, nil }
	addr = svc_addr_to_dst_addr(pool.route.SvcAddr)
	socket, err = dialer.DialContext(ctx, TcpAddrClass(addr), addr.String())
	if err != nil { return nil, err }
	socket.SetDeadline(time.Now().Add(10 * time.Second))
	stop_close = context.AfterFunc(ctx, func() { socket.Close() })
	transport, channels, requests, err = ssh.NewClientConn(socket, addr.String(), &ssh.ClientConfig{
		User: credentials.username,
		Auth: []ssh.AuthMethod{ssh.Password(credentials.password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if !stop_close() || ctx.Err() != nil {
		socket.Close()
		if transport != nil { transport.Close() }
		return nil, ctx.Err()
	}
	if err != nil { socket.Close(); return nil, err }
	socket.SetDeadline(time.Time{})
	client = ssh.NewClient(transport, channels, requests)
	if pool.leases == nil { pool.leases = make(map[*ssh.Client]int) }
	pool.clients[credentials] = client
	pool.leases[client] = 1
	return client, nil
}

func (pool *xterm_ssh_pool) release(client *ssh.Client, credentials xterm_ssh_credentials) {
	pool.mtx.Lock()
	pool.leases[client]--
	if pool.leases[client] == 0 {
		delete(pool.leases, client)
		if pool.clients[credentials] == client { delete(pool.clients, credentials) }
		pool.mtx.Unlock()
		client.Close()
		return
	}
	pool.mtx.Unlock()
}

func (pool *xterm_ssh_pool) open(parent context.Context, data []string) (*xterm_backend, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	var client *ssh.Client
	var results chan *xterm_backend
	var failures chan error
	var backend *xterm_backend
	var credentials xterm_ssh_credentials
	var err error

	ctx, cancel = context.WithTimeout(parent, 10 * time.Second)
	defer cancel()
	credentials = xterm_ssh_credentials{username: data[0], password: data[1]}
	client, err = pool.get_client(ctx, credentials)
	if err != nil { return nil, err }
	results = make(chan *xterm_backend)
	failures = make(chan error, 1)
	pool.wg.Add(1)
	go func() {
		var session *ssh.Session
		var in io.WriteCloser
		var reader *io.PipeReader
		var writer *io.PipeWriter
		var backend *xterm_backend
		var done chan struct{}
		var once sync.Once
		var stop_setup func() bool
		var open_err error

		defer pool.wg.Done()
		defer func() { if backend == nil { pool.release(client, credentials) } }()
		session, open_err = client.NewSession()
		if open_err != nil { failures <- open_err; return }
		stop_setup = context.AfterFunc(ctx, func() { session.Close() })
		defer stop_setup()
		in, open_err = session.StdinPipe()
		if open_err == nil { open_err = session.RequestPty("xterm", 25, 80, ssh.TerminalModes{}) }
		if open_err != nil { session.Close(); failures <- open_err; return }
		reader, writer = io.Pipe()
		session.Stdout = writer
		session.Stderr = writer
		open_err = session.Shell()
		if open_err != nil { session.Close(); reader.Close(); writer.Close(); failures <- open_err; return }
		done = make(chan struct{})
		pool.count(1)
		go func() { session.Wait(); writer.Close(); close(done) }()
		backend = &xterm_backend{
			in: in,
			out: reader,
			resize: func(rows int, cols int) error { return session.WindowChange(rows, cols) },
			close: func() { once.Do(func() { reader.Close(); writer.Close(); session.Close() }) },
			wait: func() { <-done; pool.count(-1); pool.release(client, credentials) },
		}
		stop_setup()
		select {
			case results <- backend:
			case <-ctx.Done():
				backend.close()
				backend.wait()
		}
	}()
	select {
		case backend = <-results:
			return backend, nil
		case err = <-failures:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
	}
}
