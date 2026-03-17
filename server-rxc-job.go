package hodu

import "encoding/base64"
import "fmt"
import "sort"
import "strconv"
import "strings"
import "sync"
import "time"
import "unsafe"

import "golang.org/x/net/websocket"

const SERVER_RXC_RUN_OUTPUT_MAX int = 65536

type ServerRxcJobMap map[uint64]*ServerRxcJob

type ServerRxcJob struct {
	Id uint64
	Kind string
	Script string
	Created time.Time

	run_mtx sync.Mutex
	run_map map[ConnId]*ServerRxcJobRun
}

type ServerRxcJobRun struct {
	Job *ServerRxcJob
	Cts *ServerConn
	ConnId ConnId
	ClientToken string
	Created time.Time
	Started time.Time
	Stopped time.Time
	RxcId uint64
	Status string
	StopMsg string
	Output []byte
	OutputTruncated bool

	mtx sync.Mutex
}

type ServerRxcSink interface {
	ReqStop()
	Write(data []byte) error
	Stop(msg string) error
}

type ServerRxcWebsocketSink struct {
	ws *websocket.Conn
}

// ServerRxcWebsocketSink - implements ServerRxcSink
func (sink *ServerRxcWebsocketSink) ReqStop() {
	if sink.ws != nil {
		sink.ws.SetReadDeadline(time.Now())
	}
}

func (sink *ServerRxcWebsocketSink) Write(data []byte) error {
	return send_ws_data_for_xterm(sink.ws, "iov", base64.StdEncoding.EncodeToString(data))
}

func (sink *ServerRxcWebsocketSink) Stop(msg string) error {
	sink.ReqStop()
	return nil
}

// ServerRxcJobRun

func new_server_rxc_job_run(job *ServerRxcJob, cts *ServerConn) *ServerRxcJobRun {
	return &ServerRxcJobRun{
		Job: job,
		Cts: cts,
		ConnId: cts.Id,
		ClientToken: cts.ClientToken.Get(),
		Created: time.Now(),
		Status: "starting",
	}
}

func (run *ServerRxcJobRun) mark_started(rxc_id uint64) {
	run.mtx.Lock()
	run.RxcId = rxc_id
	if run.Started.IsZero() {
		run.Started = time.Now()
	}
	if run.Status == "starting" {
		run.Status = "running"
	}
	run.mtx.Unlock()
}

func (run *ServerRxcJobRun) mark_start_failure(msg string) {
	run.mtx.Lock()
	run.Stopped = time.Now()
	run.Status = "failed"
	run.StopMsg = msg
	run.mtx.Unlock()
}

func (run *ServerRxcJobRun) request_stop() (*ServerConn, uint64, bool) {
	var cts *ServerConn
	var rxc_id uint64

	run.mtx.Lock()
	if run.Status == "running" {
		run.Status = "stopping"
		cts = run.Cts
		rxc_id = run.RxcId
	}
	run.mtx.Unlock()

	if cts == nil || rxc_id == 0 {
		// it wasn't originally at the running state
		return nil, 0, false
	}

	return cts, rxc_id, true
}

func (run *ServerRxcJobRun) is_done() bool {
	var status string

	run.mtx.Lock()
	status = run.Status
	run.mtx.Unlock()

	return status == "stopped" || status == "failed"
}

func (run *ServerRxcJobRun) append_output(data []byte, capture_tail bool) {
	var fit_len int
	var keep_len int
	var old_off int

	run.mtx.Lock()
	if capture_tail {
		if len(data) >= SERVER_RXC_RUN_OUTPUT_MAX {
			if len(data) > SERVER_RXC_RUN_OUTPUT_MAX || len(run.Output) > 0 {
				run.OutputTruncated = true
			}
			run.Output = append(run.Output[:0], data[len(data) - SERVER_RXC_RUN_OUTPUT_MAX:]...)
		} else {
			if len(run.Output) > SERVER_RXC_RUN_OUTPUT_MAX {
				run.Output = append([]byte(nil), run.Output[len(run.Output) - SERVER_RXC_RUN_OUTPUT_MAX:]...)
				run.OutputTruncated = true
			}

			fit_len = SERVER_RXC_RUN_OUTPUT_MAX - len(data)
			if fit_len < 0 { fit_len = 0 }
			if len(run.Output) > fit_len {
				keep_len = fit_len
				old_off = len(run.Output) - keep_len
				copy(run.Output, run.Output[old_off:])
				run.Output = run.Output[:keep_len]
				run.OutputTruncated = true
			}

			run.Output = append(run.Output, data...)
		}
	} else {
		//if len(run.Output) > SERVER_RXC_RUN_OUTPUT_MAX {
		//	run.Output = append([]byte(nil), run.Output[:SERVER_RXC_RUN_OUTPUT_MAX]...)
		//	run.OutputTruncated = true
		//}

		if !run.OutputTruncated {
			fit_len = SERVER_RXC_RUN_OUTPUT_MAX - len(run.Output)
			if fit_len <= 0 {
				run.OutputTruncated = true
			} else {
				if len(data) > fit_len {
					data = data[:fit_len]
					run.OutputTruncated = true
				}
				run.Output = append(run.Output, data...)
			}
		}
	}
	run.mtx.Unlock()
}

func (run *ServerRxcJobRun) stop(msg string) {
	run.mtx.Lock()
	if run.Status != "stopped" && run.Status != "failed" {
		run.Status = "stopped"
		run.Stopped = time.Now()
		run.StopMsg = msg
	}
	run.mtx.Unlock()
}

func (run *ServerRxcJobRun) ReqStop() {
	// this is called from run.Cts.receive_from_stream() or run.Cts.ReqStop()
	// I can assume that the cause for this stop request is "connection closed".
	// If you want to specify your own reason, you must call Stop() instead.
	run.stop("connection closed")
}

func (run *ServerRxcJobRun) Write(data []byte) error {
	run.append_output(data, false)
	return nil
}

func (run *ServerRxcJobRun) Stop(msg string) error {
	run.stop(msg)
	return nil
}


// ServerRxcJob

func (job *ServerRxcJob) snapshot_runs() []*ServerRxcJobRun {
	var runs []*ServerRxcJobRun
	var run *ServerRxcJobRun

	job.run_mtx.Lock()
	runs = make([]*ServerRxcJobRun, 0, len(job.run_map))
	for _, run = range job.run_map {
		runs = append(runs, run)
	}
	job.run_mtx.Unlock()

	sort.Slice(runs, func(i int, j int) bool { return runs[i].ConnId < runs[j].ConnId })
	return runs
}

func (job *ServerRxcJob) FindRunByConnIdStr(conn_id string) (*ServerRxcJobRun, error) {
	var conn_nid uint64
	var err error
	var run *ServerRxcJobRun

	conn_nid, err = strconv.ParseUint(conn_id, 10, int(unsafe.Sizeof(ConnId(0)) * 8))
	if err == nil {
		job.run_mtx.Lock()
		run = job.run_map[ConnId(conn_nid)]
		job.run_mtx.Unlock()
		if run == nil {
			return nil, fmt.Errorf("non-existent rxc run conn id %d", conn_nid)
		}
		return run, nil
	}

	for _, run = range job.snapshot_runs() {
		if run.ClientToken == conn_id {
			return run, nil
		}
	}

	return nil, fmt.Errorf("non-existent rxc run client %s", conn_id)
}

func (job *ServerRxcJob) is_done() bool {
	var runs []*ServerRxcJobRun
	var run *ServerRxcJobRun

	runs = job.snapshot_runs()
	for _, run = range runs {
		if !run.is_done() {
			return false
		}
	}

	return true
}

func (job *ServerRxcJob) delete_run(run *ServerRxcJobRun) bool {
	var existing *ServerRxcJobRun
	var ok bool

	job.run_mtx.Lock()
	existing, ok = job.run_map[run.ConnId]
	if ok && existing == run {
		delete(job.run_map, run.ConnId)
	}
	job.run_mtx.Unlock()

	return ok && existing == run
}

// Server
func (s *Server) snapshot_server_rxc_jobs() []*ServerRxcJob {
	var jobs []*ServerRxcJob
	var job *ServerRxcJob

	s.rxc_job_mtx.Lock()
	jobs = make([]*ServerRxcJob, 0, len(s.rxc_job_map))
	for _, job = range s.rxc_job_map {
		jobs = append(jobs, job)
	}
	s.rxc_job_mtx.Unlock()

	sort.Slice(jobs, func(i int, j int) bool { return jobs[i].Id < jobs[j].Id })
	return jobs
}

func (s *Server) FindServerRxcJobById(id uint64) *ServerRxcJob {
	var job *ServerRxcJob
	var ok bool

	s.rxc_job_mtx.Lock()
	job, ok = s.rxc_job_map[id]
	s.rxc_job_mtx.Unlock()
	if !ok { return nil }
	return job
}

func (s *Server) FindServerRxcJobByIdStr(job_id string) (*ServerRxcJob, error) {
	var job_nid uint64
	var err error
	var job *ServerRxcJob

	job_nid, err = strconv.ParseUint(job_id, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid exec job id %s - %s", job_id, err.Error())
	}

	job = s.FindServerRxcJobById(job_nid)
	if job == nil {
		return nil, fmt.Errorf("non-existent exec job id %d", job_nid)
	}

	return job, nil
}

func (s *Server) delete_server_rxc_job(job *ServerRxcJob) bool {
	var existing *ServerRxcJob
	var ok bool

	s.rxc_job_mtx.Lock()
	existing, ok = s.rxc_job_map[job.Id]
	if ok && existing == job {
		delete(s.rxc_job_map, job.Id)
	}
	s.rxc_job_mtx.Unlock()

	return ok && existing == job
}

func (s *Server) resolve_server_rxc_targets(clients []string) ([]*ServerConn, error) {
	var conns []*ServerConn
	var client string
	var cts *ServerConn
	var err error
	var dedupe map[ConnId]*ServerConn

	if len(clients) <= 0 {
		conns = s.snapshot_server_conns()
		if len(conns) <= 0 {
			return nil, fmt.Errorf("no connected clients")
		}
		return conns, nil
	}

	dedupe = make(map[ConnId]*ServerConn)
	for _, client = range clients {
		client = strings.TrimSpace(client)
		if client == "" { continue }
		cts, err = s.FindServerConnByIdStr(client)
		if err != nil { return nil, err }
		dedupe[cts.Id] = cts
	}

	if len(dedupe) <= 0 {
		return nil, fmt.Errorf("no connected clients")
	}

	conns = make([]*ServerConn, 0, len(dedupe))
	for _, cts = range dedupe {
		conns = append(conns, cts)
	}
	sort.Slice(conns, func(i int, j int) bool { return conns[i].Id < conns[j].Id })
	return conns, nil
}

func (s *Server) StartRxcJob(clients []string, kind string, script string) (*ServerRxcJob, error) {
	var conns []*ServerConn
	var cts *ServerConn
	var job *ServerRxcJob
	var run *ServerRxcJobRun
	var err error
	var ok bool

	if strings.TrimSpace(kind) == "" {
		return nil, fmt.Errorf("blank exec kind")
	}
	if strings.TrimSpace(script) == "" {
		return nil, fmt.Errorf("blank exec script")
	}

	conns, err = s.resolve_server_rxc_targets(clients)
	if err != nil { return nil, err }

	job = &ServerRxcJob{
		Kind: kind,
		Script: script,
		Created: time.Now(),
		run_map: make(map[ConnId]*ServerRxcJobRun),
	}

	s.rxc_job_mtx.Lock()
	job.Id = s.rxc_job_next_id
	s.rxc_job_next_id++
	if s.rxc_job_next_id == 0 { s.rxc_job_next_id++ }
	s.rxc_job_map[job.Id] = job
	s.rxc_job_mtx.Unlock()

	job.run_mtx.Lock()
	for _, cts = range conns {
		job.run_map[cts.Id] = new_server_rxc_job_run(job, cts)
	}
	job.run_mtx.Unlock()

	for _, cts = range conns {
		job.run_mtx.Lock()
		run, ok = job.run_map[cts.Id]
		job.run_mtx.Unlock()
		if !ok { continue }

		err = cts.StartRxcJob(run, kind, script)
		if err != nil {
			run.mark_start_failure(err.Error())
		}
	}

	return job, nil
}

func (s *Server) StopRxcJobRun(run *ServerRxcJobRun) (bool, error) {
	var cts *ServerConn
	var rxc_id uint64
	var ok bool
	var err error

	cts, rxc_id, ok = run.request_stop()
	if !ok {
		return false, nil
	}

	err = cts.SendStopRxcById(rxc_id)
	if err != nil {
		run.stop(err.Error())
		return false, err
	}

	return true, nil
}

func (s *Server) StopRxcJob(job *ServerRxcJob) int {
	var run *ServerRxcJobRun
	var runs []*ServerRxcJobRun
	var stop_count int

	job.run_mtx.Lock()
	runs = make([]*ServerRxcJobRun, 0, len(job.run_map))
	for _, run = range job.run_map {
		runs = append(runs, run)
	}
	job.run_mtx.Unlock()

	for _, run = range runs {
		var err error
		var stopped bool

		stopped, err = s.StopRxcJobRun(run)
		if stopped {
			stop_count++
		}
		if err != nil { continue }
	}

	return stop_count
}

func (s *Server) DeleteRxcJob(job *ServerRxcJob) error {
	if !job.is_done() {
		return fmt.Errorf("active rxc job id %d", job.Id)
	}
	if !s.delete_server_rxc_job(job) {
		return fmt.Errorf("non-existent rxc job id %d", job.Id)
	}

	return nil
}

func (s *Server) DeleteRxcJobRun(run *ServerRxcJobRun) error {
	if !run.is_done() {
		return fmt.Errorf("active rxc run conn id %d", run.ConnId)
	}
	if !run.Job.delete_run(run) {
		return fmt.Errorf("non-existent rxc run conn id %d", run.ConnId)
	}

	return nil
}
