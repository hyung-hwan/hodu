package hodu

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

