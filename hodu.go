package hodu

import "bytes"
import "crypto/rsa"
import _ "embed"
import "encoding/base64"
import "fmt"
import "net"
import "net/http"
import "net/netip"
import "os"
import "regexp"
import "runtime"
import "strings"
import "sync"
import "time"

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

type Named struct {
	name string
}

type Logger interface {
	Write(id string, level LogLevel, fmtstr string, args ...interface{})
	WriteWithCallDepth(id string, level LogLevel, call_depth int, fmtstr string, args ...interface{})
	Rotate()
	Close()
}

type Service interface {
	RunTask(wg *sync.WaitGroup) // blocking. run the actual task loop. it must call wg.Done() upon exit from itself.
	StartService(data interface{}) // non-blocking. spin up a service. it may be invokded multiple times for multiple instances
	StopServices() // non-blocking. send stop request to all services spun up
	FixServices() // do some fixup as needed
	WaitForTermination() // blocking. must wait until all services are stopped
	WriteLog(id string, level LogLevel, fmtstr string, args ...interface{})
}

type HttpAccessAction int
const (
	HTTP_ACCESS_ACCEPT HttpAccessAction = iota
	HTTP_ACCESS_REJECT
	HTTP_ACCESS_AUTH_REQUIRED
)

type HttpAccessRule struct {
	Prefix string
	OrgNets []netip.Prefix
	Action HttpAccessAction
}

type HttpAuthCredMap map[string]string

type HttpAuthConfig struct {
	Enabled bool
	Realm string
	Creds HttpAuthCredMap
	TokenTtl time.Duration
	TokenRsaKey *rsa.PrivateKey
	AccessRules []HttpAccessRule
}

type JsonErrmsg struct {
	Text string `json:"error-text"`
}

type json_in_notice struct {
	Text string `json:"text"`
}

type json_out_go_stats struct {
	CPUs int `json:"cpus"`
	Goroutines int `json:"goroutines"`
	NumCgoCalls int64 `json:"num-cgo-calls"`
	NumGCs uint32 `json:"num-gcs"`
	AllocBytes uint64 `json:"memory-alloc-bytes"`
	TotalAllocBytes uint64 `json:"memory-total-alloc-bytes"`
	SysBytes uint64 `json:"memory-sys-bytes"`
	Lookups uint64 `json:"memory-lookups"`
	MemAllocs uint64 `json:"memory-num-allocs"`
	MemFrees uint64 `json:"memory-num-frees"`

	HeapAllocBytes uint64 `json:"memory-heap-alloc-bytes"`
	HeapSysBytes uint64 `json:"memory-heap-sys-bytes"`
	HeapIdleBytes uint64 `json:"memory-heap-idle-bytes"`
	HeapInuseBytes uint64 `json:"memory-heap-inuse-bytes"`
	HeapReleasedBytes uint64 `json:"memory-heap-released-bytes"`
	HeapObjects uint64 `json:"memory-heap-objects"`
	StackInuseBytes uint64 `json:"memory-stack-inuse-bytes"`
	StackSysBytes uint64 `json:"memory-stack-sys-bytes"`
	MSpanInuseBytes uint64 `json:"memory-mspan-inuse-bytes"`
	MSpanSysBytes uint64 `json:"memory-mspan-sys-bytes"`
	MCacheInuseBytes uint64 `json:"memory-mcache-inuse-bytes"`
	MCacheSysBytes uint64 `json:"memory-mcache-sys-bytes"`
	BuckHashSysBytes uint64 `json:"memory-buck-hash-sys-bytes"`
	GCSysBytes uint64 `json:"memory-gc-sys-bytes"`
	OtherSysBytes uint64 `json:"memory-other-sys-bytes"`
}


type json_xterm_ws_event struct {
	Type string `json:"type"`
	Data []string `json:"data"`
}

// ---------------------------------------------------------

//go:embed xterm.js
var xterm_js []byte
//go:embed xterm-addon-fit.js
var xterm_addon_fit_js []byte
//go:embed xterm-addon-unicode11.js
var xterm_addon_unicode11_js []byte
//go:embed xterm.css
var xterm_css []byte
//go:embed xterm.html
var xterm_html string

type xterm_session_info struct {
	Mode string
	ConnId string
	RouteId string
}

// ---------------------------------------------------------

func (n *Named) SetName(name string) {
	n.name = name
}

func (n *Named) Name() string {
	return n.name
}

func TcpAddrStrClass(addr string) string {
	// the string is supposed to be addr:port

	if len(addr) > 0 {
		var ap netip.AddrPort
		var err error
		ap, err = netip.ParseAddrPort(addr)
		if err == nil {
			if ap.Addr().Is6() { return "tcp6" }
			if ap.Addr().Is4() || ap.Addr().Is4In6() { return "tcp4" }
		}
	}

	return "tcp"
}

func TcpAddrClass(addr *net.TCPAddr) string {
	var netip_addr netip.Addr
	netip_addr = addr.AddrPort().Addr()
	if netip_addr.Is4() || netip_addr.Is4In6() {
		return "tcp4"
	} else {
		return "tcp6"
	}
}

func word_to_route_option(word string) RouteOption {
	switch word {
		case "tcp4":
			return RouteOption(ROUTE_OPTION_TCP4)
		case "tcp6":
			return RouteOption(ROUTE_OPTION_TCP6)
		case "tcp":
			return RouteOption(ROUTE_OPTION_TCP)
		case "http":
			return RouteOption(ROUTE_OPTION_HTTP)
		case "https":
			return RouteOption(ROUTE_OPTION_HTTPS)
		case "ssh":
			return RouteOption(ROUTE_OPTION_SSH)
	}

	return RouteOption(ROUTE_OPTION_UNSPEC)
}

func StringToRouteOption(desc string) RouteOption {
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

func (option RouteOption) String() string {
	var str string
	str = ""
	if option & RouteOption(ROUTE_OPTION_TCP6)  != 0 { str += " tcp6" }
	if option & RouteOption(ROUTE_OPTION_TCP4)  != 0 { str += " tcp4" }
	if option & RouteOption(ROUTE_OPTION_TCP)   != 0 { str += " tcp" }
	if option & RouteOption(ROUTE_OPTION_HTTP)  != 0 { str += " http" }
	if option & RouteOption(ROUTE_OPTION_HTTPS) != 0 { str += " https" }
	if option & RouteOption(ROUTE_OPTION_SSH)   != 0 { str += " ssh" }
	if str == "" { return str }
	return str[1:] // remove the leading space
}

func dump_call_frame_and_exit(log Logger, req *http.Request, err interface{}) {
	var buf []byte
	buf = make([]byte, 65536)
	buf = buf[:min(65536, runtime.Stack(buf, false))]
	log.Write("", LOG_ERROR, "[%s] %s %s - %v\n%s", req.RemoteAddr, req.Method, req.RequestURI, err, string(buf))
	log.Close()
	os.Exit(99) // fatal error. treat panic() as a fatal runtime error
}

func svc_addr_to_dst_addr (svc_addr *net.TCPAddr) *net.TCPAddr {
	var addr net.TCPAddr

	addr = *svc_addr
	if addr.IP.To4() != nil {
		if addr.IP.IsUnspecified() {
			addr.IP = net.IPv4(127, 0, 0, 1) // net.IPv4loopback is not defined. so use net.IPv4()
		}
	} else {
		if addr.IP.IsUnspecified() {
			addr.IP = net.IPv6loopback // net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
		}
	}

	return &addr
}

func is_digit_or_period(r rune) bool {
	return (r >= '0' && r <= '9') || r == '.'
}

func get_last_rune_of_non_empty_string(s string) rune {
	var tmp []rune
	// the string must not be blank for this to work
	tmp = []rune(s)
	return tmp[len(tmp) - 1]
}

func ParseDurationString(dur string) (time.Duration, error) {
	// i want the input to be in seconds with resolution of 9 digits after
	// the decimal point. For example, 0.05 to mean 500ms.
	// however, i don't care if a unit is part of the input.
	var tmp string

	if dur == "" { return 0, nil }

	tmp = dur
	if is_digit_or_period(get_last_rune_of_non_empty_string(tmp)) { tmp = tmp + "s" }
	return time.ParseDuration(tmp)
}

func DurationToSecString(d time.Duration) string {
	return fmt.Sprintf("%.09f", d.Seconds())
}

// ------------------------------------

func WriteJsonRespHeader(w http.ResponseWriter, status_code int) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status_code)
	return status_code
}

func WriteJsRespHeader(w http.ResponseWriter, status_code int) int {
	w.Header().Set("Content-Type", "application/javascript")
	w.WriteHeader(status_code)
	return status_code
}

func WriteCssRespHeader(w http.ResponseWriter, status_code int) int {
	w.Header().Set("Content-Type", "text/css")
	w.WriteHeader(status_code)
	return status_code
}

func WriteHtmlRespHeader(w http.ResponseWriter, status_code int) int {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status_code)
	return status_code
}

func WriteEmptyRespHeader(w http.ResponseWriter, status_code int) int {
	w.WriteHeader(status_code)
	return status_code
}

// ------------------------------------

func server_route_to_proxy_info(r *ServerRoute) *ServerRouteProxyInfo {
	return &ServerRouteProxyInfo{
		SvcOption: r.SvcOption,
		PtcName: r.PtcName,
		PtcAddr: r.PtcAddr,
		SvcAddr: r.SvcAddr,
		SvcPermNet: r.SvcPermNet,
	}
}

func proxy_info_to_server_route(pi *ServerRouteProxyInfo) *ServerRoute {
	return &ServerRoute{
		SvcOption: pi.SvcOption,
		PtcName: pi.PtcName,
		PtcAddr: pi.PtcAddr,
		SvcAddr: pi.SvcAddr,
		SvcPermNet: pi.SvcPermNet,
	}
}

// ------------------------------------

func (stats *json_out_go_stats) from_runtime_stats() {
	var mstat runtime.MemStats

	runtime.ReadMemStats(&mstat)

	stats.CPUs = runtime.NumCPU()
	stats.Goroutines = runtime.NumGoroutine()
	stats.NumCgoCalls = runtime.NumCgoCall()
	stats.NumGCs = mstat.NumGC

	stats.AllocBytes = mstat.Alloc
	stats.TotalAllocBytes = mstat.TotalAlloc
	stats.SysBytes = mstat.Sys
	stats.Lookups = mstat.Lookups
	stats.MemAllocs = mstat.Mallocs
	stats.MemFrees = mstat.Frees

	stats.HeapAllocBytes = mstat.HeapAlloc
	stats.HeapSysBytes = mstat.HeapSys
	stats.HeapIdleBytes = mstat.HeapIdle
	stats.HeapInuseBytes = mstat.HeapInuse
	stats.HeapReleasedBytes = mstat.HeapReleased
	stats.HeapObjects = mstat.HeapObjects
	stats.StackInuseBytes = mstat.StackInuse
	stats.StackSysBytes = mstat.StackSys
	stats.MSpanInuseBytes = mstat.MSpanInuse
	stats.MSpanSysBytes = mstat.MSpanSys
	stats.MCacheInuseBytes = mstat.MCacheInuse
	stats.MCacheSysBytes = mstat.MCacheSys
	stats.BuckHashSysBytes = mstat.BuckHashSys
	stats.GCSysBytes = mstat.GCSys
	stats.OtherSysBytes = mstat.OtherSys
}

// ------------------------------------

func (auth *HttpAuthConfig) Authenticate(req *http.Request) (int, string) {
	var rule HttpAccessRule
	var raddrport netip.AddrPort
	var raddr netip.Addr
	var err error

	raddrport, err = netip.ParseAddrPort(req.RemoteAddr)
	if err == nil { raddr = raddrport.Addr() }

	for _, rule = range auth.AccessRules {
		// i don't take into account X-Forwarded-For and similar headers
		var pfxd string = rule.Prefix
		if pfxd[len(pfxd) -1] != '/' { pfxd = pfxd + "/" }
		if req.URL.Path == rule.Prefix || strings.HasPrefix(req.URL.Path, pfxd) {
			var org_net_ok bool

			if len(rule.OrgNets) > 0 && raddr.IsValid() {
				var netpfx netip.Prefix

				org_net_ok = false
				for  _, netpfx = range rule.OrgNets {
					if err == nil && netpfx.Contains(raddr) {
						org_net_ok = true
						break
					}
				}
			} else {
				org_net_ok = true
			}

			if org_net_ok {
				if rule.Action == HTTP_ACCESS_ACCEPT {
					return http.StatusOK, ""
				} else if rule.Action == HTTP_ACCESS_REJECT {
					return http.StatusForbidden, ""
				}

				// HTTP_ACCESS_AUTH_REQUIRED.
				// move on to authentication if enabled. acceped if disabled
				break
			}
		}
	}

	if auth != nil && auth.Enabled {
		var auth_hdr string
		var username string
		var password string
		var credpass string
		var ok bool

		auth_hdr = req.Header.Get("Authorization")
		if auth_hdr != "" {
			var auth_parts []string

			auth_parts = strings.Fields(auth_hdr)
			if len(auth_parts) == 2 && strings.EqualFold(auth_parts[0], "Bearer") && auth.TokenRsaKey != nil {
				var jwt *JWT[ServerTokenClaim]
				var claim ServerTokenClaim
				jwt = NewJWT(auth.TokenRsaKey, &claim)
				err = jwt.VerifyRS512(strings.TrimSpace(auth_parts[1]))
				if err == nil {
					// verification ok. let's check the actual payload
					var now time.Time
					now = time.Now()
					if now.After(time.Unix(claim.IssuedAt, 0)) && now.Before(time.Unix(claim.ExpiresAt, 0)) { return http.StatusOK, "" } // not expired
				}
			}
		}

		// this application wants these two header values to be base64-encoded
		username = req.Header.Get("X-Auth-Username")
		password = req.Header.Get("X-Auth-Password")
		if username != "" {
			var tmp []byte
			tmp, err = base64.StdEncoding.DecodeString(username)
			if err != nil { return http.StatusBadRequest, "" }
			username = string(tmp)
		}
		if password != "" {
			var tmp []byte
			tmp, err = base64.StdEncoding.DecodeString(password)
			if err != nil { return http.StatusBadRequest, "" }
			password = string(tmp)
		}

		// fall back to basic authentication
		if username == "" && password == "" && auth.Realm != "" {
			username, password, ok = req.BasicAuth()
			if !ok { return http.StatusUnauthorized, auth.Realm }
		}

		credpass, ok = auth.Creds[username]
		if !ok || credpass != password {
			return http.StatusUnauthorized, auth.Realm
		}
	}

	return http.StatusOK, ""
}

// ------------------------------------

func get_http_req_line_and_headers(r *http.Request, force_host bool) []byte {
	var buf bytes.Buffer
	var name string
	var value string
	var values []string
	var host_found bool
	var x_forwarded_host_found bool
	var x_forwarded_proto_found bool

	fmt.Fprintf(&buf, "%s %s %s\r\n", r.Method, r.RequestURI, r.Proto)

	for name, values = range r.Header {
		if strings.EqualFold(name, "Accept-Encoding") { // TODO: make it generic. parameterize it??
			// skip Accept-Encoding as the go client side
			// doesn't function properly when a certain enconding
			// is specified. resp.Body.Read() returned EOF when
			// not working
			continue
		} else if strings.EqualFold(name, "Host") {
			host_found = true
		} else if strings.EqualFold(name, "X-Forwarded-Host") {
			x_forwarded_host_found = true
		} else if strings.EqualFold(name, "X-Forwarded-Proto") {
			x_forwarded_proto_found = true
		}

		for _, value = range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", name, value)
		}
	}

	if force_host && !host_found && r.Host != "" {
		fmt.Fprintf(&buf, "Host: %s\r\n", r.Host)
	}
	if !x_forwarded_host_found && r.Host != "" {
		fmt.Fprintf(&buf, "X-Forwarded-Host: %s\r\n", r.Host)
	}
	if !x_forwarded_proto_found && r.Host != "" {
		var proto string
		if r.TLS != nil { proto = "https" } else { proto = "http" }
		fmt.Fprintf(&buf, "X-Forwarded-Proto: %s\r\n", proto)
	}
// TODO: host and x-forwarded-for, etc???

	buf.WriteString("\r\n") // End of headers
	return buf.Bytes()
}

func get_http_resp_line_and_headers(r *http.Response) []byte {
	var buf bytes.Buffer
	var name string
	var value string
	var values []string

	fmt.Fprintf(&buf, "%s %s\r\n", r.Proto, r.Status)

	for name, values = range r.Header {
		for _, value = range values {
			fmt.Fprintf(&buf, "%s: %s\r\n", name, value)
		}
	}

	buf.WriteString("\r\n") // End of headers
	return buf.Bytes()
}

func get_regex_submatch(re *regexp.Regexp, str string, n int) string {
	var idxs []int
	var pos int
	var start int
	var end int

	idxs = re.FindStringSubmatchIndex(str)
	if idxs == nil { return "" }

	pos = n * 2
	if pos + 1 >= len(idxs) { return "" }

	start, end = idxs[pos], idxs[pos + 1]
	if start == -1 || end == -1 {
		return ""
	}

	return str[start:end]
}
