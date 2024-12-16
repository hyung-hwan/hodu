package main

import "context"
import "crypto/tls"
import _ "embed"
import "flag"
import "fmt"
import "hodu"
import "io"
import "net"
import "os"
import "os/signal"
import "strings"
import "sync"
import "syscall"
import "time"


// Don't change these items to 'const' as they can be overridden externally with a linker option
var HODU_NAME string = "hodu"
var HODU_VERSION string = "0.0.0"

//go:embed tls.crt
var hodu_tls_cert_text []byte
//go:embed tls.key
var hodul_tls_key_text []byte

// --------------------------------------------------------------------
type signal_handler struct {
	svc hodu.Service
}

func (sh *signal_handler) RunTask(wg *sync.WaitGroup) {
	var sighup_chan  chan os.Signal
	var sigterm_chan chan os.Signal
	var sig          os.Signal

	if wg != nil {
		defer wg.Done()
	}

	sighup_chan = make(chan os.Signal, 1)
	sigterm_chan = make(chan os.Signal, 1)

	signal.Notify(sighup_chan, syscall.SIGHUP)
	signal.Notify(sigterm_chan, syscall.SIGTERM, os.Interrupt)

chan_loop:
	for {
		select {
		case <-sighup_chan:
			sh.svc.FixServices()

		case sig = <-sigterm_chan:
			sh.svc.StopServices()
			sh.svc.WriteLog ("", hodu.LOG_INFO, "Received %s signal", sig)
			break chan_loop
		}
	}

	//signal.Reset(syscall.SIGHUP)
	//signal.Reset(syscall.SIGTERM)
	signal.Stop(sighup_chan)
	signal.Stop(sigterm_chan)
}

func (sh *signal_handler) StartService(data interface{}) {
	// this isn't actually used standalone.. 
	// if we are to implement it, it must use the wait group for signal handler itself
	// however, this service is run through another service.
	// sh.wg.Add(1)
	// go sh.RunTask(&sh.wg)
}

func (sh *signal_handler) StopServices() {
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM) // TODO: find a better to terminate the signal handler...
}

func (sh *signal_handler) FixServices() {
}

func (sh *signal_handler) WaitForTermination() {
	// not implemented. see the comment in StartServices()
	// sh.wg.Wait()
}

func (sh *signal_handler) WriteLog(id string, level hodu.LogLevel, fmt string, args ...interface{}) {
	sh.svc.WriteLog(id, level, fmt, args...)
}

// --------------------------------------------------------------------

func server_main(ctl_addrs []string, rpc_addrs []string, pxy_addrs []string, cfg *ServerConfig) error {
	var s *hodu.Server
	var ctltlscfg *tls.Config
	var rpctlscfg *tls.Config
	var pxytlscfg *tls.Config
	var ctl_prefix string
	var logger *AppLogger
	var log_mask hodu.LogMask
	var logfile string
	var logfile_maxsize int64
	var logfile_rotate int
	var max_rpc_conns int
	var max_peers int
	var err error

	log_mask = hodu.LOG_ALL

	if cfg != nil {
		ctltlscfg, err = make_tls_server_config(&cfg.CTL.TLS)
		if err != nil { return err }
		rpctlscfg, err = make_tls_server_config(&cfg.RPC.TLS)
		if err != nil { return err }
		pxytlscfg, err = make_tls_server_config(&cfg.PXY.TLS)
		if err != nil { return err }

		if len(ctl_addrs) <= 0 {
			ctl_addrs = cfg.CTL.Service.Addrs
		}

		if len(rpc_addrs) <= 0 {
			rpc_addrs = cfg.RPC.Service.Addrs
		}

		if len(pxy_addrs) <= 0 {
			pxy_addrs = cfg.PXY.Service.Addrs
		}

		ctl_prefix = cfg.CTL.Service.Prefix
		log_mask = log_strings_to_mask(cfg.APP.LogMask)
		logfile = cfg.APP.LogFile
		logfile_maxsize = cfg.APP.LogMaxSize
		logfile_rotate = cfg.APP.LogRotate
		max_rpc_conns = cfg.APP.MaxRpcConns
		max_peers = cfg.APP.MaxPeers
	}

	if len(rpc_addrs) <= 0 {
		return fmt.Errorf("no rpc service addresses specified")
	}

	if logfile == "" {
		logger = NewAppLogger("server", os.Stderr, log_mask)
	} else {
		logger, err = NewAppLoggerToFile("server", logfile, logfile_maxsize, logfile_rotate, log_mask)
		if err != nil {
			return fmt.Errorf("failed to initialize logger - %s", err.Error())
		}
	}

	s, err = hodu.NewServer(
		context.Background(),
		logger,
		ctl_addrs,
		rpc_addrs,
		pxy_addrs,
		ctl_prefix,
		ctltlscfg,
		rpctlscfg,
		pxytlscfg,
		max_rpc_conns,
		max_peers)
	if err != nil {
		return fmt.Errorf("failed to create new server - %s", err.Error())
	}

	s.StartService(nil)
	s.StartCtlService()
	s.StartPxyService()
	s.StartExtService(&signal_handler{svc:s}, nil)
	s.WaitForTermination()
	logger.Close()

	return nil
}

// --------------------------------------------------------------------

func parse_client_route_config(v string) (*hodu.ClientRouteConfig, error) {
	var va []string
	var ptc_name string
	var svc_addr string
	var option hodu.RouteOption
	var port string
	var err error

	va = strings.Split(v, ",")

	if len(va) <= 0 { return nil, fmt.Errorf("blank value") }
	if len(va) >= 5 { return nil, fmt.Errorf("too many fields in %v", v) }

	_, port, err = net.SplitHostPort(strings.TrimSpace(va[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid client-side peer address [%s] - %s", va[0], err.Error())
	}

	if len(va) >= 2 {
		var f string
		f = strings.TrimSpace(va[1])
		_, _, err = net.SplitHostPort(f)
		if err != nil {
			return nil, fmt.Errorf("invalid server-side service address [%s] - %s", va[1], err.Error())
		}
		svc_addr = f
	}

	option = hodu.RouteOption(hodu.ROUTE_OPTION_TCP)
	if len(va) >= 3 {
		switch strings.ToLower(strings.TrimSpace(va[2])) {
			case "ssh":
				option |= hodu.RouteOption(hodu.ROUTE_OPTION_SSH)
			case "http":
				option |= hodu.RouteOption(hodu.ROUTE_OPTION_HTTP)
			case "https":
				option |= hodu.RouteOption(hodu.ROUTE_OPTION_HTTPS)

			case "":
				fallthrough
			case "auto":
				// automatic determination of protocol for common ports
				switch port {
					case "22":
						option |= hodu.RouteOption(hodu.ROUTE_OPTION_SSH)
					case "80":
						option |= hodu.RouteOption(hodu.ROUTE_OPTION_HTTP)
					case "443":
						option |= hodu.RouteOption(hodu.ROUTE_OPTION_HTTPS)
				}
			default:
				return nil, fmt.Errorf("invalid option value %s", va[2])
		}
	}

	if len(va) >= 4 {
		ptc_name = strings.TrimSpace(va[3])
	}

	return &hodu.ClientRouteConfig{PeerAddr: va[0], PeerName: ptc_name, Option: option, ServiceAddr: svc_addr}, nil // TODO: other fields
}

func client_main(ctl_addrs []string, rpc_addrs []string, route_configs []string, cfg *ClientConfig) error {
	var c *hodu.Client
	var ctltlscfg *tls.Config
	var rpctlscfg *tls.Config
	var ctl_prefix string
	var cc hodu.ClientConfig
	var logger *AppLogger
	var log_mask hodu.LogMask
	var logfile string
	var logfile_maxsize int64
	var logfile_rotate int
	var max_rpc_conns int
	var max_peers int
	var peer_conn_tmout time.Duration
	var i int
	var err error

	log_mask = hodu.LOG_ALL
	if cfg != nil {
		ctltlscfg, err = make_tls_server_config(&cfg.CTL.TLS)
		if err != nil {
			return err
		}
		rpctlscfg, err = make_tls_client_config(&cfg.RPC.TLS)
		if err != nil {
			return err
		}

		if len(ctl_addrs) <= 0 { ctl_addrs = cfg.CTL.Service.Addrs }
		if len(rpc_addrs) <= 0 { rpc_addrs = cfg.RPC.Endpoint.Addrs }
		ctl_prefix = cfg.CTL.Service.Prefix

		cc.ServerSeedTmout = cfg.RPC.Endpoint.SeedTmout
		cc.ServerAuthority = cfg.RPC.Endpoint.Authority
		log_mask = log_strings_to_mask(cfg.APP.LogMask)
		logfile = cfg.APP.LogFile
		logfile_maxsize = cfg.APP.LogMaxSize
		logfile_rotate = cfg.APP.LogRotate
		max_rpc_conns = cfg.APP.MaxRpcConns
		max_peers = cfg.APP.MaxPeers
		peer_conn_tmout = cfg.APP.PeerConnTmout
	}

	// unlke the server, we allow the client to start with no rpc address.
	// no check if len(rpc_addrs) <= 0 is mdde here.
	cc.ServerAddrs = rpc_addrs
	cc.Routes = make([]hodu.ClientRouteConfig, len(route_configs))
	for i, _ = range route_configs {
		var c *hodu.ClientRouteConfig
		c, err = parse_client_route_config(route_configs[i])
		if err != nil { return err }
		cc.Routes[i] = *c
	}

	if logfile == "" {
		logger = NewAppLogger("server", os.Stderr, log_mask)
	} else {
		logger, err = NewAppLoggerToFile("server", logfile, logfile_maxsize, logfile_rotate, log_mask)
		if err != nil {
			return fmt.Errorf("failed to initialize logger - %s", err.Error())
		}
	}
	c = hodu.NewClient(
		context.Background(),
		logger,
		ctl_addrs,
		ctl_prefix,
		ctltlscfg,
		rpctlscfg,
		max_rpc_conns,
		max_peers,
		peer_conn_tmout)

	c.StartService(&cc)
	c.StartCtlService() // control channel
	c.StartExtService(&signal_handler{svc:c}, nil) // signal handler task
	c.WaitForTermination()
	logger.Close()

	return nil
}

func main() {
	var err error
	var flgs *flag.FlagSet

	if len(os.Args) < 2 { goto wrong_usage }

	if strings.EqualFold(os.Args[1], "server") {
		var rpc_addrs []string
		var ctl_addrs []string
		var pxy_addrs []string
		var cfgfile string
		var logfile string
		var cfg *ServerConfig

		ctl_addrs = make([]string, 0)
		rpc_addrs = make([]string, 0)
		pxy_addrs = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("ctl-on", "specify a listening address for control channel", func(v string) error {
			ctl_addrs = append(ctl_addrs, v)
			return nil
		})
		flgs.Func("rpc-on", "specify a rpc listening address", func(v string) error {
			rpc_addrs = append(rpc_addrs, v)
			return nil
		})
		flgs.Func("pxy-on", "specify a proxy listening address", func(v string) error {
			pxy_addrs = append(pxy_addrs, v)
			return nil
		})
		flgs.Func("log-file", "specify a log file", func(v string) error {
			logfile = v
			return nil
		})
		flgs.Func("config-file", "specify a configuration file path", func(v string) error {
			cfgfile = v
			return nil
		})
		// TODO: add a command line option to specify log file and mask.
		flgs.SetOutput(io.Discard) // prevent usage output
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if flgs.NArg() > 0 { goto wrong_usage }

		if (cfgfile != "") {
			cfg, err = load_server_config(cfgfile)
			if err != nil {
				fmt.Printf ("ERROR: failed to load configuration file %s - %s\n", cfgfile, err.Error())
				goto oops
			}
		}
		if logfile != "" { cfg.APP.LogFile = logfile }

		err = server_main(ctl_addrs, rpc_addrs, pxy_addrs, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: server error - %s\n", err.Error())
			goto oops
		}
	} else if strings.EqualFold(os.Args[1], "client") {
		var rpc_addrs []string
		var ctl_addrs []string
		var cfgfile string
		var logfile string
		var cfg *ClientConfig

		ctl_addrs = make([]string, 0)
		rpc_addrs = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("ctl-on", "specify a listening address for control channel", func(v string) error {
			ctl_addrs = append(ctl_addrs, v)
			return nil
		})
		flgs.Func("rpc-server", "specify a rpc server address", func(v string) error {
			rpc_addrs = append(rpc_addrs, v)
			return nil
		})
		flgs.Func("log-file", "specify a log file", func(v string) error {
			logfile = v
			return nil
		})
		flgs.Func("config-file", "specify a configuration file path", func(v string) error {
			cfgfile = v
			return nil
		})
		// TODO: add a command line option to specify log file and mask.
		flgs.SetOutput(io.Discard)
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if cfgfile != "" {
			cfg, err = load_client_config(cfgfile)
			if err != nil {
				fmt.Printf ("ERROR: failed to load configuration file %s - %s\n", cfgfile, err.Error())
				goto oops
			}
		}
		if logfile != "" { cfg.APP.LogFile = logfile }

		err = client_main(ctl_addrs, rpc_addrs, flgs.Args(), cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: client error - %s\n", err.Error())
			goto oops
		}
	} else if strings.EqualFold(os.Args[1], "version") {
		if len(os.Args) != 2 { goto wrong_usage }
		fmt.Printf("%s %s\n", HODU_NAME, HODU_VERSION)
	} else {
		goto wrong_usage
	}

	os.Exit(0)

wrong_usage:
	fmt.Fprintf(os.Stderr, "USAGE: %s server --rpc-on=addr:port --ctl-on=addr:port --pxy-on=addr:port [--config-file=file]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s client --rpc-server=addr:port --ctl-on=addr:port [--config-file=file] [peer-addr:peer-port ...]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s version\n", os.Args[0])
	os.Exit(1)

oops:
	os.Exit(1)
}
