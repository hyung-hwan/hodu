package hodu

import "net/http"
import "os"
import "runtime"
import "sync"

const HODU_RPC_VERSION uint32 = 0x010000

type LogLevel int

const (
	LOG_DEBUG LogLevel = iota + 1
	LOG_ERROR
	LOG_WARN
	LOG_INFO
)

type Logger interface {
	Write (id string, level LogLevel, fmtstr string, args ...interface{})
}

type Service interface {
	RunTask (wg *sync.WaitGroup) // blocking. run the actual task loop. it must call wg.Done() upon exit from itself.
	StartService(data interface{}) // non-blocking. spin up a service. it may be invokded multiple times for multiple instances
	StopServices() // non-blocking. send stop request to all services spun up
	WaitForTermination() // blocking. must wait until all services are stopped
	WriteLog(id string, level LogLevel, fmtstr string, args ...interface{})
}

func tcp_addr_str_class(addr string) string {
	if len(addr) > 0 {
		switch addr[0] {
			case '[':
				return "tcp6"
			case ':':
				return "tcp"
			default:
				return "tcp4"
		}
	}

	return "tcp"
}

func dump_call_frame_and_exit(log Logger, req *http.Request, err interface{}) {
	var buf []byte
	buf = make([]byte, 65536); buf = buf[:min(65536, runtime.Stack(buf, false))]
	log.Write("", LOG_ERROR, "[%s] %s %s - %v\n%s", req.RemoteAddr, req.Method, req.URL.String(), err, string(buf))
	os.Exit(99) // fatal error. treat panic() as a fatal runtime error
}
