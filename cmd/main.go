package main

import "context"
import "crypto/tls"
import "crypto/x509"
import "flag"
import "fmt"
import "hodu"
import "io"
import "io/ioutil"
import "os"
import "os/signal"
import "path/filepath"
import "runtime"
import "strings"
import "sync"
import "syscall"
import "time"


// --------------------------------------------------------------------

const rootKey = `-----BEGIN EC PARAMETERS-----
BggqhkjOPQMBBw==
-----END EC PARAMETERS-----
-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIHg+g2unjA5BkDtXSN9ShN7kbPlbCcqcYdDu+QeV8XWuoAoGCCqGSM49
AwEHoUQDQgAEcZpodWh3SEs5Hh3rrEiu1LZOYSaNIWO34MgRxvqwz1FMpLxNlx0G
cSqrxhPubawptX5MSr02ft32kfOlYbaF5Q==
-----END EC PRIVATE KEY-----
`

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
// --------------------------------------------------------------------

type AppLogger struct {
	id string
	out io.Writer
	mtx sync.Mutex
}

func (l* AppLogger) Write(id string, level hodu.LogLevel, fmtstr string, args ...interface{}) {
	var now time.Time
	var off_m int
	var off_h int
	var off_s int
	var hdr string
	var msg string
	var lid string
	var caller_file string
	var caller_line int
	var caller_ok bool

// TODO: do something with level
	now = time.Now()

	_, off_s = now.Zone()
	off_m = off_s / 60;
	off_h = off_m / 60;
	off_m = off_m % 60;
	if (off_m < 0) { off_m = -off_m; }

	hdr = fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d %+03d%02d ",
		now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), off_h, off_m)

	_, caller_file, caller_line, caller_ok = runtime.Caller(1)

// TODO: add pid?
	msg = fmt.Sprintf(fmtstr, args...)
	if id == "" {
		lid = fmt.Sprintf("%s: ", l.id)
	} else {
		lid = fmt.Sprintf("%s(%s): ", l.id, id)
	}

	l.mtx.Lock()
	l.out.Write([]byte(hdr))
	if caller_ok {
		l.out.Write([]byte(fmt.Sprintf("[%s:%d] ", filepath.Base(caller_file), caller_line)))
	}
	if lid != "" { l.out.Write([]byte(lid)) }
	l.out.Write([]byte(msg))
	if msg[len(msg) - 1] != '\n' { l.out.Write([]byte("\n")) }
	l.mtx.Unlock()
}

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
			// TODO:
			//sh.svc.ReqReload()
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

func (sh *signal_handler) WaitForTermination() {
	// not implemented. see the comment in StartServices()
	// sh.wg.Wait()
}

func (sh *signal_handler) WriteLog(id string, level hodu.LogLevel, fmt string, args ...interface{}) {
	sh.svc.WriteLog(id, level, fmt, args...)
}

func tls_string_to_client_auth_type(str string) tls.ClientAuthType {
	switch str {
		case tls.NoClientCert.String():
			return tls.NoClientCert
		case tls.RequestClientCert.String():
			return tls.RequestClientCert
		case tls.RequireAnyClientCert.String():
			return tls.RequireAnyClientCert
		case tls.VerifyClientCertIfGiven.String():
			return tls.VerifyClientCertIfGiven
		case tls.RequireAndVerifyClientCert.String():
			return tls.RequireAndVerifyClientCert
		default:
			return tls.NoClientCert
	}
}

// --------------------------------------------------------------------

func make_server_tls_config(cfg *ServerTLSConfig) (*tls.Config, error) {
	var tlscfg *tls.Config

	if cfg.Enabled {
		var cert tls.Certificate
		var cert_pool *x509.CertPool
		var err error

		if cfg.CertText != "" && cfg.KeyText != "" {
			cert, err = tls.X509KeyPair([]byte(cfg.CertText), []byte(cfg.KeyText))
		} else if cfg.CertFile != "" && cfg.KeyFile != "" {
			cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		} else {
			// use the embedded certificate
			cert, err = tls.X509KeyPair([]byte(rootCert), []byte(rootKey))
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load key pair - %s", err)
		}

		if cfg.ClientCACertText != "" || cfg.ClientCACertFile != ""{
			var ok bool

			cert_pool = x509.NewCertPool()

			if cfg.ClientCACertText != "" {
				ok = cert_pool.AppendCertsFromPEM([]byte(cfg.ClientCACertText))
				if !ok {
					return nil, fmt.Errorf("failed to append certificate to pool")
				}
			} else if cfg.ClientCACertFile != "" {
				var text []byte
				text, err = ioutil.ReadFile(cfg.ClientCACertFile)
				if err != nil {
					return nil, fmt.Errorf("failed to load client ca certficate file %s - %s", cfg.ClientCACertFile, err.Error())
				}
				ok = cert_pool.AppendCertsFromPEM(text)
				if !ok {
					return nil, fmt.Errorf("failed to append certificate to pool")
				}
			}
		}

	/*
		// Don't use `Certificates` it doesn't work with some certificate files.
		// See, `getClientCertificate` in ${GOSRC}/src/crypto/tls/handshake_client.go for details
		tlsConfig.GetClientCertificate = func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			 return cert, nil
		}
	*/
		tlscfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth: tls_string_to_client_auth_type(cfg.ClientAuthType),
			ClientCAs: cert_pool, // trusted CA certs for client certificate verification
			//ServerName: "hodu",
		}
	}

	return tlscfg, nil
}

func server_main(ctl_addrs []string, svcaddrs []string, cfg *ServerConfig) error {
	var s *hodu.Server
	var tlscfg *tls.Config
	var err error

	tlscfg, err = make_server_tls_config(&cfg.TLS)
	if err != nil {
		return err
	}

	s, err = hodu.NewServer(
		context.Background(),
		ctl_addrs,
		svcaddrs,
		&AppLogger{id: "server", out: os.Stderr},
		tlscfg)
	if err != nil {
		return fmt.Errorf("failed to create new server - %s", err.Error())
	}

	s.StartService(nil)
	s.StartCtlService()
	s.StartExtService(&signal_handler{svc:s}, nil)
	s.WaitForTermination()

	return nil
}

// --------------------------------------------------------------------

func client_main(ctl_addrs []string, server_addr string, peer_addrs []string, cfg *ClientConfig) error {
	var c *hodu.Client
	var tlscfg *tls.Config
	var cc hodu.ClientConfig
	var err error

	tlscfg, err = make_server_tls_config(&cfg.TLS)
	if err != nil {
		return err
	}

	c = hodu.NewClient(
		context.Background(),
		ctl_addrs,
		&AppLogger{id: "client", out: os.Stderr},
		tlscfg)

	cc.ServerAddr = server_addr
	cc.PeerAddrs = peer_addrs

	c.StartService(&cc)
	c.StartCtlService() // control channel
	c.StartExtService(&signal_handler{svc:c}, nil) // signal handler task
	c.WaitForTermination()

	return nil
}

func main() {
	var err error
	var flgs *flag.FlagSet

	if len(os.Args) < 2 {
		goto wrong_usage
	}
	if strings.EqualFold(os.Args[1], "server") {
		var rpc_addrs[] string
		var ctl_addrs[] string
		var cfgfile string
		var cfg *ServerConfig

		ctl_addrs = make([]string, 0)
		rpc_addrs = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("ctl-on", "specify a listening address for control channel", func(v string) error {
			ctl_addrs = append(ctl_addrs, v)
			return nil
		})
		flgs.Func("rpc-on", "specify a rpc listening address", func(v string) error {
			rpc_addrs = append(rpc_addrs, v)
			return nil
		})
		flgs.Func("config-file", "specify a configuration file path", func(v string) error {
			cfgfile = v
			return nil
		})
		flgs.SetOutput(io.Discard) // prevent usage output
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if len(rpc_addrs) < 1 || flgs.NArg() > 0 {
			goto wrong_usage
		}

		if (cfgfile != "") {
			cfg, err = LoadServerConfig(cfgfile)
			if err != nil {
				fmt.Printf ("ERROR: failed to load configuration file %s - %s\n", cfgfile, err.Error())
				goto oops
			}
		}

		err = server_main(ctl_addrs, rpc_addrs, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: server error - %s\n", err.Error())
			goto oops
		}
	} else if strings.EqualFold(os.Args[1], "client") {
		var rpc_addrs []string
		var ctl_addrs []string
		var cfgfile string
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
		flgs.Func("config-file", "specify a configuration file path", func(v string) error {
			cfgfile = v
			return nil
		})
		flgs.SetOutput(io.Discard)
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if len(rpc_addrs) < 1 {
			goto wrong_usage
		}

		if (cfgfile != "") {
			cfg, err = LoadClientConfig(cfgfile)
			if err != nil {
				fmt.Printf ("ERROR: failed to load configuration file %s - %s\n", cfgfile, err.Error())
				goto oops
			}
		}

		err = client_main(ctl_addrs, rpc_addrs[0], flgs.Args(), cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: client error - %s\n", err.Error())
			goto oops
		}
	} else {
		goto wrong_usage
	}

	os.Exit(0)

wrong_usage:
	fmt.Fprintf(os.Stderr, "USAGE: %s server --rpc-on=addr:port --ctl-on=addr:port \n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s client --rpc-server=addr:port --ctl-on=addr:port  [peer-addr:peer-port ...]\n", os.Args[0])
	os.Exit(1)

oops:
	os.Exit(1)
}
