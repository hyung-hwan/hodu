package hodu

import "sync"

const HODU_VERSION uint32 = 0x010000

type LogLevel int

const (
	LOG_DEBUG LogLevel = iota + 1
	LOG_ERROR
	LOG_WARN
	LOG_INFO
)

type Logger interface {
	Write (level LogLevel, fmt string, args ...interface{})
}

type Service interface {
	RunTask (wg *sync.WaitGroup) // blocking. run the actual task loop
	StartService(data interface{}) // non-blocking. spin up a service. it may be invokded multiple times for multiple instances
	StopServices() // non-blocking. send stop request to all services spun up
	WaitForTermination() // blocking. must wait until all services are stopped
}

type ExtTask func(svc Service, wg *sync.WaitGroup)
