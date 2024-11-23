package main

import "context"
import "crypto/tls"
import "crypto/x509"
import "flag"
import "fmt"
import "hodu"
import "io"
import "log"
import "os"
import "os/signal"
import "strings"
import "sync"
import "syscall"


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

type serverLogger struct {
	log *log.Logger
}


func (log* serverLogger) Write(level hodu.LogLevel, fmt string, args ...interface{}) {
	log.log.Printf(fmt, args...)
}


// --------------------------------------------------------------------
type signal_handler struct {
	svc hodu.Service
}

func (sh *signal_handler) RunTask(wg *sync.WaitGroup) {
	var sighup_chan  chan os.Signal
	var sigterm_chan chan os.Signal
	var sig          os.Signal

	defer wg.Done()

	sighup_chan = make(chan os.Signal, 1)
	sigterm_chan = make(chan os.Signal, 1)

	signal.Notify(sighup_chan, syscall.SIGHUP)
	signal.Notify(sigterm_chan, syscall.SIGTERM, os.Interrupt)

chan_loop:
	for {
		select {
		case <-sighup_chan:
			// TODO:
			//svc.ReqReload()
		case sig = <-sigterm_chan:
			// TODO: get timeout value from config
			//c.Shutdown(fmt.Sprintf("termination by signal %s", sig), 3*time.Second)
			sh.svc.StopServices()
			//log.Debugf("termination by signal %s", sig)
fmt.Printf("termination by signal %s\n", sig)
			break chan_loop
		}
	}
	
	//signal.Reset(syscall.SIGHUP)
	//signal.Reset(syscall.SIGTERM)
	signal.Stop(sighup_chan)
	signal.Stop(sigterm_chan)
fmt.Printf("end of signal handler\n")
}

func (sh *signal_handler) StartService(data interface{}) {
	// this isn't actually used standalone.. 
	// if we are to implement it, it must use the wait group for signal handler itself
	// however, this service is run through another service.
	//
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

func server_main(laddrs []string) error {
	var s *hodu.Server
	var err error

	var sl serverLogger
	var cert tls.Certificate

	cert, err = tls.X509KeyPair([]byte(rootCert), []byte(rootKey))
	if err != nil {
		return fmt.Errorf("ERROR: failed to load key pair - %s\n", err)
	}

	sl.log = log.Default()
	s, err = hodu.NewServer(laddrs, &sl, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return fmt.Errorf("ERROR: failed to create new server - %s", err.Error())
	}

	s.StartService(nil)
	s.StartExtService(&signal_handler{svc:s}, nil)
	s.WaitForTermination()

	return nil
}

// --------------------------------------------------------------------

func client_main(listen_on string, server_addr string, peer_addrs []string) error {
	var c *hodu.Client
	var cert_pool *x509.CertPool
	var tlscfg *tls.Config
	var cc hodu.ClientConfig

	cert_pool = x509.NewCertPool()
	ok := cert_pool.AppendCertsFromPEM([]byte(rootCert))
	if !ok {
		log.Fatal("failed to parse root certificate")
	}
	tlscfg = &tls.Config{
		RootCAs: cert_pool,
		ServerName: "localhost",
		InsecureSkipVerify: true,
	}

	c = hodu.NewClient(context.Background(), listen_on, tlscfg)

	cc.ServerAddr = server_addr
	cc.PeerAddrs = peer_addrs

	c.StartService(&cc)
	c.StartCtlService()
	c.StartExtService(&signal_handler{svc:c}, nil)
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
		var la []string

		la = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("listen-on", "specify a listening address", func(v string) error {
			la = append(la, v)
			return nil
		})
		flgs.SetOutput(io.Discard) // prevent usage output
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if len(la) < 0 || flgs.NArg() > 0 {
			goto wrong_usage
		}

		err = server_main(la)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: server error - %s\n", err.Error())
			goto oops
		}
	} else if strings.EqualFold(os.Args[1], "client") {
		var la []string
		var sa []string

		la = make([]string, 0)
		sa = make([]string, 0)

		flgs = flag.NewFlagSet("", flag.ContinueOnError)
		flgs.Func("listen-on", "specify a control channel address", func(v string) error {
			la = append(la, v)
			return nil
		})
		flgs.Func("server", "specify a server address", func(v string) error {
			sa = append(sa, v)
			return nil
		})
		flgs.SetOutput(io.Discard)
		err = flgs.Parse(os.Args[2:])
		if err != nil {
			fmt.Printf ("ERROR: %s\n", err.Error())
			goto wrong_usage
		}

		if len(la) != 1 || len(sa) != 1 || flgs.NArg() < 1 {
			goto wrong_usage
		}
		err = client_main(la[0], sa[0], flgs.Args())
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: client error - %s\n", err.Error())
			goto oops
		}
	} else {
		goto wrong_usage
	}

	os.Exit(0)

wrong_usage:
	fmt.Fprintf(os.Stderr, "USAGE: %s server --listen-on=addr:port\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "       %s client --listen-on=addr:port --server=addr:port peer-addr:peer-port\n", os.Args[0])
	os.Exit(1)

oops:
	os.Exit(1)
}
