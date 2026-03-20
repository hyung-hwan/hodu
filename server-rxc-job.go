package hodu

import "container/heap"
import "encoding/base64"
import "fmt"
import "sort"
import "strconv"
import "strings"
import "sync"
import "time"
import "unsafe"

import "golang.org/x/net/websocket"

const SERVER_RXC_RUN_OUTPUT_MAX int = 1024
const SERVER_RXC_DONE_JOB_RETENTION time.Duration = 60 * time.Second

const SERVER_RXC_RUN_STATUS_STARTING string = "starting"
const SERVER_RXC_RUN_STATUS_RUNNING string = "running"
const SERVER_RXC_RUN_STATUS_STOPPING string = "stopping"
const SERVER_RXC_RUN_STATUS_STOPPED string = "stopped"
const SERVER_RXC_RUN_STATUS_FAILED string = "failed"

type ServerRxcJob struct {
	S *Server
	Id uint64
	Type string
	Script string
	Created time.Time
	Done time.Time

	expire_at time.Time
	heap_index int

	run_mtx sync.Mutex
	run_map map[ConnId]*ServerRxcJobRun
	active_run_count int
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

	Output [2][]byte
	OutputTruncated [2]bool

	mtx sync.Mutex
}

type ServerRxcJobMap map[uint64]*ServerRxcJob

type ServerRxcJobExpiryHeap []*ServerRxcJob

type ServerRxcSink interface {
	ReqStop()
	Write(flags uint32, data []byte) error
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

func (sink *ServerRxcWebsocketSink) Write(flags uint32, data []byte) error {
	return send_ws_data_for_xterm(sink.ws, "iov", base64.StdEncoding.EncodeToString(data))
}

func (sink *ServerRxcWebsocketSink) Stop(msg string) error {
	sink.ReqStop()
	return nil
}

/* implement heap.Interface for ServerRxcJobExpiryMap */
func (heap ServerRxcJobExpiryHeap) Len() int {
	return len(heap)
}

func (heap ServerRxcJobExpiryHeap) Less(i int, j int) bool {
	if heap[i].expire_at.Before(heap[j].expire_at) { return true }
	if heap[j].expire_at.Before(heap[i].expire_at) { return false }
	return heap[i].Id < heap[j].Id
}

func (heap ServerRxcJobExpiryHeap) Swap(i int, j int) {
	var x *ServerRxcJob
	var y *ServerRxcJob

	x = heap[i]
	y = heap[j]
	heap[i] = y
	heap[j] = x
	if heap[i] != nil { heap[i].heap_index = i }
	if heap[j] != nil { heap[j].heap_index = j }
}

func (heap *ServerRxcJobExpiryHeap) Push(x interface{}) {
	var job *ServerRxcJob

	job = x.(*ServerRxcJob)
	job.heap_index = len(*heap)
	*heap = append(*heap, job)
}

func (heap *ServerRxcJobExpiryHeap) Pop() interface{} {
	var old ServerRxcJobExpiryHeap
	var n int
	var job *ServerRxcJob

	old = *heap
	n = len(old)
	job = old[n - 1]
	old[n - 1] = nil
	job.heap_index = -1
	*heap = old[:n - 1]
	return job
}

// ServerRxcJobRun

func new_server_rxc_job_run(job *ServerRxcJob, cts *ServerConn) *ServerRxcJobRun {
	return &ServerRxcJobRun{
		Job: job,
		Cts: cts,
		ConnId: cts.Id,
		ClientToken: cts.ClientToken.Get(),
		Created: time.Now(),
		Status: SERVER_RXC_RUN_STATUS_STARTING,
	}
}

func (run *ServerRxcJobRun) mark_started(rxc_id uint64) {
	run.mtx.Lock()
	run.RxcId = rxc_id
	if run.Started.IsZero() {
		run.Started = time.Now()
	}
	if run.Status == SERVER_RXC_RUN_STATUS_STARTING {
		run.Status = SERVER_RXC_RUN_STATUS_RUNNING
	}
	run.mtx.Unlock()
}

func (run *ServerRxcJobRun) mark_start_failure(msg string) {
	var transitioned bool

	run.mtx.Lock()
	if run.Status != SERVER_RXC_RUN_STATUS_STOPPED && run.Status != SERVER_RXC_RUN_STATUS_FAILED {
		run.Stopped = time.Now()
		run.Status = SERVER_RXC_RUN_STATUS_FAILED
		run.StopMsg = msg
		transitioned = true
	}
	run.mtx.Unlock()

	if transitioned {
		if run.Cts.S.maybe_mark_rxc_job_done(run.Job) {
			run.Cts.S.notify_rxc_job_purge()
		}
	}
}

func (run *ServerRxcJobRun) request_stop() (*ServerConn, uint64, bool) {
	var cts *ServerConn
	var rxc_id uint64

	run.mtx.Lock()
	if run.Status == SERVER_RXC_RUN_STATUS_RUNNING {
		run.Status = SERVER_RXC_RUN_STATUS_STOPPING
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

	return status == SERVER_RXC_RUN_STATUS_STOPPED || status == SERVER_RXC_RUN_STATUS_FAILED
}

func (run *ServerRxcJobRun) append_output(data []byte, outidx int, capture_tail bool) {
	var fit_len int
	var keep_len int
	var old_off int
	var output_max int

	output_max = run.Job.S.Cfg.RxcRunOutputMax
	if output_max <= 0 { return } // don't capture

	run.mtx.Lock()
	if capture_tail {
		if len(data) >= output_max {
			if len(data) > output_max || len(run.Output[outidx]) > 0 {
				run.OutputTruncated[outidx] = true
			}
			run.Output[outidx] = append(run.Output[outidx][:0], data[len(data) - output_max:]...)
		} else {
			if len(run.Output[outidx]) > output_max {
				run.Output[outidx] = append([]byte(nil), run.Output[outidx][len(run.Output[outidx]) - output_max:]...)
				run.OutputTruncated[outidx] = true
			}

			fit_len = output_max - len(data)
			if fit_len < 0 { fit_len = 0 }
			if len(run.Output[outidx]) > fit_len {
				keep_len = fit_len
				old_off = len(run.Output[outidx]) - keep_len
				copy(run.Output[outidx], run.Output[outidx][old_off:])
				run.Output[outidx] = run.Output[outidx][:keep_len]
				run.OutputTruncated[outidx]= true
			}

			run.Output[outidx] = append(run.Output[outidx], data...)
		}
	} else {
		//if len(run.Output[outidx]) > output_max {
		//	run.Output[outidx] = append([]byte(nil), run.Output[outidx][:output_max]...)
		//	run.OutputTruncate[outidx]d = true
		//}

		if !run.OutputTruncated[outidx] {
			fit_len = output_max - len(run.Output[outidx])
			if fit_len <= 0 {
				run.OutputTruncated[outidx] = true
			} else {
				if len(data) > fit_len {
					data = data[:fit_len]
					run.OutputTruncated[outidx] = true
				}
				run.Output[outidx] = append(run.Output[outidx], data...)
			}
		}
	}
	run.mtx.Unlock()
}

func (run *ServerRxcJobRun) stop(msg string) {
	var transitioned bool

	run.mtx.Lock()
	if run.Status != SERVER_RXC_RUN_STATUS_STOPPED && run.Status != SERVER_RXC_RUN_STATUS_FAILED {
		run.Status = SERVER_RXC_RUN_STATUS_STOPPED
		run.Stopped = time.Now()
		run.StopMsg = msg
		transitioned = true
	}
	run.mtx.Unlock()

	if transitioned {
		if run.Cts.S.maybe_mark_rxc_job_done(run.Job) {
			run.Cts.S.notify_rxc_job_purge()
		}
	}
}

func (run *ServerRxcJobRun) ReqStop() {
	// this is called from run.Cts.receive_from_stream() or run.Cts.ReqStop()
	// I can assume that the cause for this stop request is "connection closed".
	// If you want to specify your own reason, you must call Stop() instead.
	run.stop("connection closed")
}

func (run *ServerRxcJobRun) Write(flags uint32, data []byte) error {
	run.append_output(data, int(flags & 0x1), false) // TODO: use a defined flag bit
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
	var done bool

	job.run_mtx.Lock()
	done = (job.active_run_count <= 0)
	job.run_mtx.Unlock()
	return done
}

func (s *Server) maybe_mark_rxc_job_done(job *ServerRxcJob) bool {
	var existing *ServerRxcJob
	var ok bool

	job.run_mtx.Lock()
	if job.active_run_count > 0 {
		job.active_run_count--
		if job.active_run_count > 0 {
			job.run_mtx.Unlock()
			return false
		}
	}
	job.run_mtx.Unlock()

	s.rxc_job_mtx.Lock()
	existing, ok = s.rxc_job_map[job.Id]
	if !ok || existing != job {
		s.rxc_job_mtx.Unlock()
		return false
	}
	if !job.Done.IsZero() {
		s.rxc_job_mtx.Unlock()
		return false
	}
	job.Done = time.Now()
	if s.Cfg.RxcDoneJobRetention > 0 {
		job.expire_at = job.Done.Add(s.Cfg.RxcDoneJobRetention)
		heap.Push(&s.rxc_job_heap, job)
	}
	s.rxc_job_mtx.Unlock()

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

func (s *Server) delete_server_rxc_job_from_heap_no_lock(job *ServerRxcJob) {
	if job.heap_index >= 0 {
		heap.Remove(&s.rxc_job_heap, job.heap_index)
		job.expire_at = time.Time{}
		//job.heap_index = -1 // skip this as it's set in s.rxc_job_heap.Pop() called by heap.Remove()
	}
}

func (s *Server) delete_server_rxc_job_no_lock(job *ServerRxcJob) bool {
	var existing *ServerRxcJob
	var ok bool

	existing, ok = s.rxc_job_map[job.Id]
	if ok && existing == job {
		delete(s.rxc_job_map, job.Id)
		s.delete_server_rxc_job_from_heap_no_lock(job)
	}

	return ok && existing == job
}

func (s *Server) delete_server_rxc_job(job *ServerRxcJob) bool {
	var deleted bool

	s.rxc_job_mtx.Lock()
	deleted = s.delete_server_rxc_job_no_lock(job)
	s.rxc_job_mtx.Unlock()

	return deleted
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

func (s *Server) StartRxcJob(clients []string, type_ string, script string) (*ServerRxcJob, error) {
	var conns []*ServerConn
	var cts *ServerConn
	var job *ServerRxcJob
	var run *ServerRxcJobRun
	var err error
	var ok bool

	if strings.TrimSpace(type_) == "" {
		return nil, fmt.Errorf("blank rxc type")
	}
	if strings.TrimSpace(script) == "" {
		return nil, fmt.Errorf("blank rxc script")
	}

	conns, err = s.resolve_server_rxc_targets(clients)
	if err != nil { return nil, err }

	job = &ServerRxcJob{
		S: s,
		Type: type_,
		Script: script,
		Created: time.Now(),
		heap_index: -1,
		run_map: make(map[ConnId]*ServerRxcJobRun),
		active_run_count: len(conns),
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

		err = cts.RunRxcJob(run, type_, script)
		if err != nil {
			run.mark_start_failure(err.Error())
			s.log.Write(cts.Sid, LOG_DEBUG, "Failed to run rxc job(%d) on client(%s) from %s(%s)", job.Id, cts.ClientToken.Get(), cts.RemoteAddr)
		} else {
			s.log.Write(cts.Sid, LOG_DEBUG, "Ran rxc job(%d) on client(%s) from %s(%s)", job.Id, cts.ClientToken.Get(), cts.RemoteAddr)
		}
	}

	s.log.Write("", LOG_DEBUG, "Started rxc job(%d) %s(%s)", job.Id, type_, script)
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

	err = cts.SendStopRxcById(rxc_id, 0) // TODO: set flags properly...
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

	s.log.Write("", LOG_DEBUG, "Stopped rxc job(%d) after stopping %d runs", job.Id, stop_count)
	return stop_count
}

func (s *Server) DeleteRxcJob(job *ServerRxcJob) error {
	if !job.is_done() {
		return fmt.Errorf("active rxc job id %d", job.Id)
	}
	if !s.delete_server_rxc_job(job) {
		return fmt.Errorf("non-existent rxc job id %d", job.Id)
	}
	if s.Cfg.RxcDoneJobRetention > 0 {
		// a job is already over and is actually deleted by request.
		// the purge goroutine needs to re-schedule the next automatic purge.
		s.notify_rxc_job_purge()
	}

	s.log.Write("", LOG_DEBUG, "Deleted rxc job(%d)", job.Id)
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

func (s *Server) purge_stale_rxc_jobs(now time.Time, expiry time.Duration) int {
	var job *ServerRxcJob
	var purge_count int

	if expiry <= 0 { return 0 }

	s.rxc_job_mtx.Lock()
	for {
		if len(s.rxc_job_heap) <= 0 { break }
		job = s.rxc_job_heap[0]
		if now.Before(job.expire_at) { break }
		if s.delete_server_rxc_job_no_lock(job) {
			s.log.Write("", LOG_INFO, "Purged stale rxc job(%d)", job.Id)
			purge_count++
		} else {
			// this must not happen. but if it happens, it is an internal error and
			// the job expiry heap and the job map are already out of sync
			s.log.Write("", LOG_WARN, "Failed to purge stale rxc job(%d): heap/map mismatch", job.Id)
			// but still attempt to delete it from the heap to prevent future purge blockage
			s.delete_server_rxc_job_from_heap_no_lock(job)
		}
	}
	s.rxc_job_mtx.Unlock()

	return purge_count
}

func (s *Server) notify_rxc_job_purge() {
	select {
		case s.rxc_job_purge_chan <- struct{}{}:
			// attempt to write to the channel
		default:
			// if not writable, do nothing and return immediately
	}
}

func (s *Server) get_next_rxc_job_purge_time(expiry time.Duration) (time.Time, bool) {
	var next time.Time

	if expiry <= 0 { return time.Time{}, false }

	s.rxc_job_mtx.Lock()
	if len(s.rxc_job_heap) <= 0 {
		s.rxc_job_mtx.Unlock()
		return time.Time{}, false
	}
	next = s.rxc_job_heap[0].expire_at
	s.rxc_job_mtx.Unlock()

	return next, true
}

func stop_and_drain_timer(timer *time.Timer) {
	if timer == nil { return }
	if !timer.Stop() {
		select {
			case <-timer.C:
			default:
		}
	}
}

func (s *Server) run_rxc_job_purger(wg *sync.WaitGroup, expiry time.Duration) {
	var timer *time.Timer
	var timer_chan <-chan time.Time
	var next time.Time
	var now time.Time
	var delay time.Duration
	var ok bool

	defer wg.Done()

	// it's best if the caller ensures expiry > 0
	if expiry <= 0 { return }

	for {
		next, ok = s.get_next_rxc_job_purge_time(expiry)
		if ok {
			delay = time.Until(next)
			if delay < 0 { delay = 0 }
			if timer == nil {
				timer = time.NewTimer(delay)
			} else {
				stop_and_drain_timer(timer)
				timer.Reset(delay)
			}
			timer_chan = timer.C
		} else {
			stop_and_drain_timer(timer)
			timer_chan = nil
		}

		select {
			case <-s.rxc_job_purge_chan:
				// a job was done and/or deleted.
				// go to the top of the loop to re-calculate when to
				// run the automatic purge next time.
				continue

			case now = <-timer_chan:
				s.purge_stale_rxc_jobs(now, expiry)

			case <-s.Ctx.Done():
				stop_and_drain_timer(timer)
				return
		}
	}
}
