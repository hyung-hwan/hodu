package main

import "context"
import _ "embed"
import "flag"
import "fmt"
import "hodu"
import "io"
import "net"
import "os"
import "os/signal"
import "path/filepath"
import "strings"
import "sync"
import "syscall"

// Don't change these items to 'const' as they can be overridden externally with a linker option
var HODU_NAME string = "hodu"
var HODU_VERSION string = "0.0.0"

//go:embed tls.crt
var hodu_tls_cert_text []byte
//go:embed tls.key
var hodu_tls_key_text []byte
//go:embed rsa.key
var hodu_rsa_key_text []byte

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

func server_main(ctl_addrs []string, rpc_addrs []string, pxy_addrs []string, wpx_addrs []string, logfile string, cfg *ServerConfig) error {
	var s *hodu.Server
	var config *hodu.ServerConfig
	var logger *AppLogger
	var logmask hodu.LogMask
	var logfile_maxsize int64
	var logfile_rotate int
	var xterm_html_file string
	var xterm_html string
	var err error

	logmask = hodu.LOG_ALL

	config = &hodu.ServerConfig{
		CtlAddrs: ctl_addrs,
		RpcAddrs: rpc_addrs,
		PxyAddrs: pxy_addrs,
		WpxAddrs: wpx_addrs,
	}

	if cfg != nil {
		config.CtlTls, err = make_tls_server_config(&cfg.CTL.TLS)
		if err != nil { return err }
		config.RpcTls, err = make_tls_server_config(&cfg.RPC.TLS)
		if err != nil { return err }
		config.PxyTls, err = make_tls_server_config(&cfg.PXY.TLS)
		if err != nil { return err }
		config.WpxTls, err = make_tls_server_config(&cfg.WPX.TLS)
		if err != nil { return err }

		if len(config.CtlAddrs) <= 0 { config.CtlAddrs = cfg.CTL.Service.Addrs }
		if len(config.RpcAddrs) <= 0 { config.RpcAddrs = cfg.RPC.Service.Addrs }
		if len(config.PxyAddrs) <= 0 { config.PxyAddrs = cfg.PXY.Service.Addrs }
		if len(config.WpxAddrs) <= 0 { config.WpxAddrs = cfg.WPX.Service.Addrs }

		config.CtlCors = cfg.CTL.Service.Cors
		config.CtlAuth, err = make_http_auth_config(&cfg.CTL.Service.Auth)
		if err != nil { return err }

		config.CtlPrefix = cfg.CTL.Service.Prefix
		config.RpcMaxConns = cfg.APP.MaxRpcConns
		config.MaxPeers = cfg.APP.MaxPeers
		xterm_html_file = cfg.APP.XtermHtmlFile

		logmask = log_strings_to_mask(cfg.APP.LogMask)
		if logfile != "" { cfg.APP.LogFile = logfile }
		logfile_maxsize = cfg.APP.LogMaxSize
		logfile_rotate = cfg.APP.LogRotate
	}

	if len(config.RpcAddrs) <= 0 {
		return fmt.Errorf("no rpc service addresses specified")
	}

	if logfile == "" {
		logger = NewAppLogger("server", os.Stderr, logmask)
	} else {
		logger, err = NewAppLoggerToFile("server", logfile, logfile_maxsize, logfile_rotate, logmask)
		if err != nil {
			return fmt.Errorf("failed to initialize logger - %s", err.Error())
		}
	}

	if xterm_html_file != "" {
		var tmp []byte
		tmp, err = os.ReadFile(xterm_html_file)
		if err != nil {
			return fmt.Errorf("failed to read %s - %s", xterm_html_file, err.Error())
		}
		xterm_html = string(tmp)
	}

	s, err = hodu.NewServer(context.Background(), HODU_NAME, logger, config)
	if err != nil {
		return fmt.Errorf("failed to create server - %s", err.Error())
	}

	if xterm_html != "" { s.SetXtermHtml(xterm_html) }

	s.StartService(nil)
	s.StartCtlService()
	s.StartPxyService()
	s.StartWpxService()
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

	return &hodu.ClientRouteConfig{PeerAddr: va[0], PeerName: ptc_name, ServiceOption: option, ServiceAddr: svc_addr}, nil // TODO: other fields
}

func client_main(ctl_addrs []string, rpc_addrs []string, route_configs []string, logfile string, cfg *ClientConfig) error {
	var c *hodu.Client
	var config *hodu.ClientConfig
	var cc hodu.ClientConnConfig
	var logger *AppLogger
	var logmask hodu.LogMask
	var logfile_maxsize int64
	var logfile_rotate int
	var pts_user string
	var pts_shell string
	var xterm_pts_html_file string
	var xterm_pts_html string
	var i int
	var err error

	logmask = hodu.LOG_ALL

	config = &hodu.ClientConfig{
		CtlAddrs: ctl_addrs,
	}

	if cfg != nil {
		config.CtlTls, err = make_tls_server_config(&cfg.CTL.TLS)
		if err != nil { return err }
		config.RpcTls, err = make_tls_client_config(&cfg.RPC.TLS)
		if err != nil { return err }

		if len(rpc_addrs) <= 0 { rpc_addrs = cfg.RPC.Endpoint.Addrs }
		if len(config.CtlAddrs) <= 0 { config.CtlAddrs = cfg.CTL.Service.Addrs }

		config.CtlPrefix = cfg.CTL.Service.Prefix
		config.CtlCors = cfg.CTL.Service.Cors
		config.CtlAuth, err = make_http_auth_config(&cfg.CTL.Service.Auth)
		if err != nil { return err }

		cc.ServerSeedTmout = cfg.RPC.Endpoint.SeedTmout
		cc.ServerAuthority = cfg.RPC.Endpoint.Authority
		logmask = log_strings_to_mask(cfg.APP.LogMask)
		if logfile == "" { logfile = cfg.APP.LogFile }
		logfile_maxsize = cfg.APP.LogMaxSize
		logfile_rotate = cfg.APP.LogRotate
		pts_user = cfg.APP.PtsUser
		pts_shell = cfg.APP.PtsShell
		xterm_pts_html_file = cfg.APP.XtermPtsHtmlFile
		config.RpcConnMax = cfg.APP.MaxRpcConns
		config.PeerConnMax = cfg.APP.MaxPeers
		config.PeerConnTmout = cfg.APP.PeerConnTmout
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
		logger = NewAppLogger("client", os.Stderr, logmask)
	} else {
		logger, err = NewAppLoggerToFile("client", logfile, logfile_maxsize, logfile_rotate, logmask)
		if err != nil {
			return fmt.Errorf("failed to initialize logger - %s", err.Error())
		}
	}

	if xterm_pts_html_file != "" {
		var tmp []byte
		tmp, err = os.ReadFile(xterm_pts_html_file)
		if err != nil {
			return fmt.Errorf("failed to read %s - %s", xterm_pts_html_file, err.Error())
		}
		xterm_pts_html = string(tmp)
	}

	c = hodu.NewClient(context.Background(), HODU_NAME, logger, config)

	if pts_user != "" { c.SetPtsUser(pts_user) }
	if pts_shell != "" { c.SetPtsShell(pts_shell) }
	if xterm_pts_html != "" { c.SetXtermPtsHtml(xterm_pts_html) }

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
		var wpx_addrs []string
		var cfgfile string
		var cfgpat string
		var logfile string
		var cfg ServerConfig

		ctl_addrs = make([]string, 0)
		rpc_addrs = make([]string, 0)
		pxy_addrs = make([]string, 0)
		wpx_addrs = make([]string, 0)

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
		flgs.Func("wpx-on", "specify a wpx listening address", func(v string) error {
			wpx_addrs = append(wpx_addrs, v)
			return nil
		})
		flgs.Func("log-file", "specify a log file", func(v string) error {
			logfile = v
			return nil
		})
		flgs.Func("config-file", "specify a primary configuration file path", func(v string) error {
			cfgfile = v
			return nil
		})
		flgs.Func("config-file-pattern", "specify a file pattern for additional configuration files", func(v string) error {
			cfgpat = v
			return nil
		})
		flgs.SetOutput(io.Discard) // prevent usage output
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if flgs.NArg() > 0 { goto wrong_usage }

		if cfgfile != "" {
			err = load_server_config_to(cfgfile, &cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to load configuration file %s - %s\n", cfgfile, err.Error())
				goto oops
			}
		}
		if cfgpat != "" {
			var file string
			var matches []string

			matches, err = filepath.Glob(cfgpat)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to match the pattern %s - %s\n", cfgpat, err.Error())
				goto oops
			}

			for _, file = range matches {
				err = load_server_config_to(file, &cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: failed to load configuration file %s - %s\n", file, err.Error())
					goto oops
				}
			}
		}

		err = server_main(ctl_addrs, rpc_addrs, pxy_addrs, wpx_addrs, logfile, &cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: server error - %s\n", err.Error())
			goto oops
		}
	} else if strings.EqualFold(os.Args[1], "client") {
		var rpc_addrs []string
		var ctl_addrs []string
		var cfgfile string
		var cfgpat string
		var logfile string
		var cfg ClientConfig

		ctl_addrs = make([]string, 0)
		rpc_addrs = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("ctl-on", "specify a listening address for control channel", func(v string) error {
			ctl_addrs = append(ctl_addrs, v)
			return nil
		})
		flgs.Func("rpc-to", "specify a rpc server address to connect to", func(v string) error {
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
		flgs.Func("config-file-pattern", "specify a file pattern for additional configuration files", func(v string) error {
			cfgpat = v
			return nil
		})
		flgs.SetOutput(io.Discard)
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if cfgfile != "" {
			err = load_client_config_to(cfgfile, &cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to load configuration file %s - %s\n", cfgfile, err.Error())
				goto oops
			}
		}
		if cfgpat != "" {
			var file string
			var matches []string

			matches, err = filepath.Glob(cfgpat)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to match the pattern %s - %s\n", cfgpat, err.Error())
				goto oops
			}

			for _, file = range matches {
				err = load_client_config_to(file, &cfg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: failed to load configuration file %s - %s\n", file, err.Error())
					goto oops
				}
			}
		}

		err = client_main(ctl_addrs, rpc_addrs, flgs.Args(), logfile, &cfg)
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
	fmt.Fprintf(os.Stderr, "USAGE: %s server --rpc-on=addr:port --ctl-on=addr:port --pxy-on=addr:port --wpx-on=addr:port [--config-file=file] [--config-file-pattern=pattern]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s client --rpc-to=addr:port --ctl-on=addr:port [--config-file=file] [--config-file-pattern=pattern] [peer-addr:peer-port ...]\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s version\n", os.Args[0])
	os.Exit(1)

oops:
	os.Exit(1)
}
