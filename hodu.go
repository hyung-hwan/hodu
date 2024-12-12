package hodu

import "net/http"
import "net/netip"
import "os"
import "runtime"
import "strings"
import "sync"


const HODU_RPC_VERSION uint32 = 0x010000

type LogLevel int
type LogMask int

const (
	LOG_DEBUG LogLevel = 1 << iota
	LOG_INFO
	LOG_WARN
	LOG_ERROR
)

const LOG_ALL LogMask = LogMask(LOG_DEBUG | LOG_INFO | LOG_WARN | LOG_ERROR)
const LOG_NONE LogMask = LogMask(0)

var IPV4_PREFIX_ZERO = netip.MustParsePrefix("0.0.0.0/0")
var IPV6_PREFIX_ZERO = netip.MustParsePrefix("::/0")

type Logger interface {
	Write(id string, level LogLevel, fmtstr string, args ...interface{})
}

type Service interface {
	RunTask(wg *sync.WaitGroup) // blocking. run the actual task loop. it must call wg.Done() upon exit from itself.
	StartService(data interface{}) // non-blocking. spin up a service. it may be invokded multiple times for multiple instances
	StopServices() // non-blocking. send stop request to all services spun up
	WaitForTermination() // blocking. must wait until all services are stopped
	WriteLog(id string, level LogLevel, fmtstr string, args ...interface{})
}

func tcp_addr_str_class(addr string) string {
	// the string is supposed to be addr:port

	if len(addr) > 0 {
		var ap netip.AddrPort
		var err error
		ap, err = netip.ParseAddrPort(addr)
		if err == nil {
			if ap.Addr().Is6() { return "tcp6" }
			if ap.Addr().Is4() { return "tcp4" }
		}
	}

	return "tcp"
}

func word_to_route_option(word string) RouteOption {
	switch word {
		case "tcp4":
			return RouteOption(ROUTE_OPTION_TCP4)
		case "tcp6":
			return RouteOption(ROUTE_OPTION_TCP6)
		case "tcp":
			return RouteOption(ROUTE_OPTION_TCP)
		case "tty":
			return RouteOption(ROUTE_OPTION_TTY)
		case "http":
			return RouteOption(ROUTE_OPTION_HTTP)
		case "https":
			return RouteOption(ROUTE_OPTION_HTTPS)
		case "ssh":
			return RouteOption(ROUTE_OPTION_SSH)
	}

	return RouteOption(ROUTE_OPTION_UNSPEC)
}

func string_to_route_option(desc string) RouteOption {
	var fld string
	var option RouteOption
	var p RouteOption

	option = RouteOption(0)
	for _, fld = range strings.Fields(desc) {
		p = word_to_route_option(fld)
		if p == RouteOption(ROUTE_OPTION_UNSPEC) { return p }
		option |= p
	}
	return option
}

func (option RouteOption) string() string {
	var str string
	str = ""
	if option & RouteOption(ROUTE_OPTION_TCP6)  != 0 { str += " tcp6" }
	if option & RouteOption(ROUTE_OPTION_TCP4)  != 0 { str += " tcp4" }
	if option & RouteOption(ROUTE_OPTION_TCP)   != 0 { str += " tcp" }
	if option & RouteOption(ROUTE_OPTION_TTY)   != 0 { str += " tty" }
	if option & RouteOption(ROUTE_OPTION_HTTP)  != 0 { str += " http" }
	if option & RouteOption(ROUTE_OPTION_HTTPS) != 0 { str += " https" }
	if option & RouteOption(ROUTE_OPTION_SSH)   != 0 { str += " ssh" }
	if str == "" { return str }
	return str[1:] // remove the leading space
}

func dump_call_frame_and_exit(log Logger, req *http.Request, err interface{}) {
	var buf []byte
	buf = make([]byte, 65536); buf = buf[:min(65536, runtime.Stack(buf, false))]
	log.Write("", LOG_ERROR, "[%s] %s %s - %v\n%s", req.RemoteAddr, req.Method, req.URL.String(), err, string(buf))
	os.Exit(99) // fatal error. treat panic() as a fatal runtime error
}
