package hodu


import "encoding/base64"
import "encoding/json"
import "fmt"
import "net/http"
import "strconv"
import "sync"
import "time"

import "golang.org/x/net/websocket"

// rxc - remote exec
type server_rxc_ws struct {
	S *Server
	Id string
	ws *websocket.Conn
}

func (rxc *server_rxc_ws) Identity() string {
	return rxc.Id
}

func (rxc *server_rxc_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var token string
	var cts *ServerConn
	var rp *ServerRxc
	var wg sync.WaitGroup
	var err error

	s = rxc.S
	req = ws.Request()
	token = req.FormValue("client-token")
	if token == "" {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("no client token specified")
	}

	cts = s.FindServerConnByClientToken(token)
	if cts == nil {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("invalid client token - %s", token)
	}

ws_recv_loop:
	for {
		var msg []byte
		err = websocket.Message.Receive(ws, &msg)
		if err != nil {
			s.log.Write(rxc.Id, LOG_ERROR, "[%s] websocket receive error - %s", req.RemoteAddr, err.Error())
			goto done
		}

		if len(msg) > 0 {
			var ev json_xterm_ws_event
			err = json.Unmarshal(msg, &ev)
			if err == nil {
				switch ev.Type {
					case "open":
						if rp == nil && len(ev.Data) == 2 {
							var type_ string
							var script string

							type_ = string(ev.Data[0])
							script = string(ev.Data[1])
							//options = string(ev.Data[2])

							rp, err = cts.StartRxcForWs(ws, type_, script)
							if err != nil {
								s.log.Write(rxc.Id, LOG_ERROR, "[%s] Failed to connect rxc - %s", req.RemoteAddr, err.Error())
								send_ws_data_for_xterm(ws, "error", err.Error())
								//ws.Close() // dirty way to flag out the error by making websocket.Message.Receive() fail
								ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
							} else {
								err = send_ws_data_for_xterm(ws, "status", "opened")
								if err != nil {
									s.log.Write(rxc.Id, LOG_ERROR, "[%s] Failed to write 'opened' event to websocket - %s", req.RemoteAddr, err.Error())
									//ws.Close() // dirty way to flag out the error
									ws.SetReadDeadline(time.Now()) // slightly cleaner way to break the main loop
								} else {
									s.log.Write(rxc.Id, LOG_DEBUG, "[%s] Opened rxc session", req.RemoteAddr)
								}
							}
						}

					case "close":
						// just break out of the loop and let the remainder to close resources
						break ws_recv_loop

					case "iov":
						var i int
						for i, _ = range ev.Data {
							var bytes []byte
							bytes, err = base64.StdEncoding.DecodeString(ev.Data[i])
							if err != nil {
								s.log.Write(rxc.Id, LOG_WARN, "[%s] Invalid rxc iov data received - %s", req.RemoteAddr, ev.Data[i])
							} else {
								cts.WriteRxcForWs(ws, bytes)
								// ignore error for now
							}
						}

					default:
						send_ws_data_for_xterm(ws, "error", fmt.Sprintf("invalid rxc event type - %s", ev.Type));
						s.log.Write(rxc.Id, LOG_WARN, "[%s] Invalid rxc event type received - %s", req.RemoteAddr, ev.Type)
				}
			}
		}
	}

done:
	cts.StopRxcForWs(ws)
	ws.Close() // don't care about multiple closes

	wg.Wait()
	s.log.Write(rxc.Id, LOG_DEBUG, "[%s] Ended rxc session for %s", req.RemoteAddr, token)

	return http.StatusOK, err
}

// HTTP control endpoints for RXC jobs:
//   GET    /_rxc
//   POST   /_rxc
//   GET    /_rxc/{job_id}
//   DELETE /_rxc/{job_id}
//   POST   /_rxc/{job_id}/stop
//   GET    /_rxc/{job_id}/runs
//   GET    /_rxc/{job_id}/runs/{conn_id}
//   DELETE /_rxc/{job_id}/runs/{conn_id}
//   POST   /_rxc/{job_id}/runs/{conn_id}/stop
//
// The websocket endpoint /_rxc/ws is kept for interactive single-client RXC.

/*
• Example to run a command on all connected clients:

 curl -X POST http://127.0.0.1:9999/_rxc \
	-H 'Content-Type: application/json' \
	-d '{
		"type": "bash",
		"script": "uname -a"
	}'

 Example to run on specific clients only:

curl -X POST http://127.0.0.1:9999/_rxc \
	-H 'Content-Type: application/json' \
	-d '{
		"clients": ["1", "client-token-abc"],
		"type": "bash",
		"script": "hostname; id"
	}'

 Then inspect the created job:

 curl http://127.0.0.1:9999/_rxc/1
 curl http://127.0.0.1:9999/_rxc/1/runs
 curl http://127.0.0.1:9999/_rxc/1/runs/1

 Stop a job:

 curl -X POST http://127.0.0.1:9999/_rxc/1/stop

 Stop one run only:

 curl -X POST http://127.0.0.1:9999/_rxc/1/runs/1/stop

 Delete a retained job record:

 curl -X DELETE http://127.0.0.1:9999/_rxc/1

 Delete a retained run record:

 curl -X DELETE http://127.0.0.1:9999/_rxc/1/runs/1

 If your control server uses a prefix, prepend it, for example http://127.0.0.1:9999/api/_rxc.
*/

type json_in_server_rxc struct {
	Clients []string `json:"clients"`
	Type string `json:"type"`
	Script string `json:"script"`
}

type json_out_server_rxc_job struct {
	JobId uint64 `json:"job-id"`
	Type string `json:"type"`
	Script string `json:"script"`
	Status string `json:"status"`
	CreatedMilli int64 `json:"created-milli"`
	DoneMilli int64 `json:"done-milli"`
	TargetCount int `json:"target-count"`
	RunningCount int `json:"running-count"`
	StoppingCount int `json:"stopping-count"`
	StoppedCount int `json:"stopped-count"`
	FailedCount int `json:"failed-count"`
}

type json_out_server_rxc_run struct {
	JobId uint64 `json:"job-id"`
	ConnId ConnId `json:"conn-id"`
	ClientToken string `json:"client-token"`
	RxcId uint64 `json:"rxc-id"`
	Status string `json:"status"`
	ExitCode int `json:"exit-code"`
	StopMsg string `json:"stop-msg"`
	CreatedMilli int64 `json:"created-milli"`
	StartedMilli int64 `json:"started-milli"`
	StoppedMilli int64 `json:"stopped-milli"`
	Stdout []byte `json:"stdout"`
	StdoutTruncated bool `json:"stdout-truncated"`
	Stderr []byte `json:"stderr"`
	StderrTruncated bool `json:"stderr-truncated"`
}

type server_ctl_rxc struct {
	ServerCtl
}

type server_ctl_rxc_id struct {
	ServerCtl
}

type server_ctl_rxc_id_stop struct {
	ServerCtl
}

type server_ctl_rxc_id_runs struct {
	ServerCtl
}

type server_ctl_rxc_id_runs_id struct {
	ServerCtl
}

type server_ctl_rxc_id_runs_id_stop struct {
	ServerCtl
}

func server_rxc_job_to_json(job *ServerRxcJob) json_out_server_rxc_job {
	var js json_out_server_rxc_job
	var done time.Time

	js.JobId = job.Id
	js.Type = job.Type
	js.Script = job.Script
	js.CreatedMilli = job.Created.UnixMilli()
	done = job.get_done_time()
	if !done.IsZero() { js.DoneMilli = done.UnixMilli() }

	job.run_mtx.Lock()
	js.TargetCount = len(job.run_map)
	js.RunningCount = job.running_run_count
	js.StoppingCount = job.stopping_run_count
	js.StoppedCount = job.stopped_run_count
	js.FailedCount = job.failed_run_count
	job.run_mtx.Unlock()

	switch {
		case js.TargetCount <= 0:
			js.Status = "done"
		case js.RunningCount > 0 || js.StoppingCount > 0:
			js.Status = "running"
		case js.StoppedCount > 0 || js.FailedCount > 0:
			js.Status = "done"
		default:
			js.Status = "starting"
	}

	return js
}

func server_rxc_run_to_json(run *ServerRxcJobRun, with_output bool) json_out_server_rxc_run {
	var js json_out_server_rxc_run

	run.mtx.Lock()
	js.JobId = run.Job.Id
	js.ConnId = run.ConnId
	js.ClientToken = run.ClientToken
	js.RxcId = run.RxcId
	js.Status = run.Status
	js.ExitCode = GetRxcStopExitCode(run.StopFlags)
	js.StopMsg = run.StopMsg
	js.CreatedMilli = run.Created.UnixMilli()
	if !run.Started.IsZero() { js.StartedMilli = run.Started.UnixMilli() }
	if !run.Stopped.IsZero() { js.StoppedMilli = run.Stopped.UnixMilli() }
	js.Stdout = make([]byte, 0) // i don't want to show nil when empty
	js.Stderr = make([]byte, 0) // i don't want to show nil when empty
	js.StdoutTruncated = run.OutputTruncated[0]
	if with_output { js.Stdout = append(js.Stdout, run.Output[0]...) }
	js.StderrTruncated = run.OutputTruncated[1]
	if with_output { js.Stderr = append(js.Stderr, run.Output[1]...) }
	run.mtx.Unlock()

	return js
}

func (ctl *server_ctl_rxc) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var err error

	s = ctl.S
	je = json.NewEncoder(w)

	switch req.Method {
		case http.MethodGet:
			var jobs []json_out_server_rxc_job
			var job *ServerRxcJob

			jobs = make([]json_out_server_rxc_job, 0)
			for _, job = range s.snapshot_server_rxc_jobs() {
				jobs = append(jobs, server_rxc_job_to_json(job))
			}

			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(jobs); err != nil { goto oops }

		case http.MethodPost:
			var x json_in_server_rxc
			var job *ServerRxcJob

			err = json.NewDecoder(req.Body).Decode(&x)
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
				goto oops
			}

			job, err = s.StartRxcJob(x.Clients, x.Type, x.Script)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}

			status_code = WriteJsonRespHeader(w, http.StatusAccepted)
			if err = je.Encode(server_rxc_job_to_json(job)); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

	return status_code, nil

oops:
	return status_code, err
}

func (ctl *server_ctl_rxc_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var job_id string
	var job *ServerRxcJob
	var err error

	s = ctl.S
	je = json.NewEncoder(w)
	job_id = req.PathValue("job_id")

	job, err = s.FindServerRxcJobByIdStr(job_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(server_rxc_job_to_json(job)); err != nil { goto oops }

		case http.MethodDelete:
			err = s.DeleteRxcJob(job)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusConflict)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

	return status_code, nil

oops:
	return status_code, err
}

func (ctl *server_ctl_rxc_id_stop) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var job_id string
	var job *ServerRxcJob
	var stop_count int
	var err error

	s = ctl.S
	je = json.NewEncoder(w)
	job_id = req.PathValue("job_id")

	job, err = s.FindServerRxcJobByIdStr(job_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodPost:
			stop_count = s.StopRxcJob(job)
			if stop_count > 0 {
				status_code = WriteEmptyRespHeader(w, http.StatusAccepted)
			} else {
				status_code = WriteEmptyRespHeader(w, http.StatusNoContent)
			}

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

	return status_code, nil

oops:
	return status_code, err
}

func (ctl *server_ctl_rxc_id_runs) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var job_id string
	var job *ServerRxcJob
	var run *ServerRxcJobRun
	var runs []json_out_server_rxc_run
	var err error

	s = ctl.S
	je = json.NewEncoder(w)
	job_id = req.PathValue("job_id")

	job, err = s.FindServerRxcJobByIdStr(job_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			var output string
			var with_output bool

			output = req.URL.Query().Get("output")
			if output != "" {
				with_output, err = strconv.ParseBool(output)
				if err != nil {
					status_code = WriteJsonRespHeader(w, http.StatusBadRequest)
					je.Encode(JsonErrmsg{Text: fmt.Sprintf("invalid output parameter - %s", output)})
					goto oops
				}
			}
			runs = make([]json_out_server_rxc_run, 0)
			for _, run = range job.snapshot_runs() {
				runs = append(runs, server_rxc_run_to_json(run, with_output))
			}
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(runs); err != nil { goto oops }

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

	return status_code, nil

oops:
	return status_code, err
}

func (ctl *server_ctl_rxc_id_runs_id) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var job_id string
	var conn_id string
	var job *ServerRxcJob
	var run *ServerRxcJobRun
	var err error

	s = ctl.S
	je = json.NewEncoder(w)
	job_id = req.PathValue("job_id")
	conn_id = req.PathValue("conn_id")

	job, err = s.FindServerRxcJobByIdStr(job_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	run, err = job.FindRunByConnIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodGet:
			status_code = WriteJsonRespHeader(w, http.StatusOK)
			if err = je.Encode(server_rxc_run_to_json(run, true)); err != nil { goto oops }

		case http.MethodDelete:
			err = s.DeleteRxcJobRun(run)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusConflict)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}
			status_code = WriteEmptyRespHeader(w, http.StatusNoContent)

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

	return status_code, nil

oops:
	return status_code, err
}

func (ctl *server_ctl_rxc_id_runs_id_stop) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var je *json.Encoder
	var job_id string
	var conn_id string
	var job *ServerRxcJob
	var run *ServerRxcJobRun
	var stopped bool
	var err error

	s = ctl.S
	je = json.NewEncoder(w)
	job_id = req.PathValue("job_id")
	conn_id = req.PathValue("conn_id")

	job, err = s.FindServerRxcJobByIdStr(job_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	run, err = job.FindRunByConnIdStr(conn_id)
	if err != nil {
		status_code = WriteJsonRespHeader(w, http.StatusNotFound)
		je.Encode(JsonErrmsg{Text: err.Error()})
		goto oops
	}

	switch req.Method {
		case http.MethodPost:
			stopped, err = s.StopRxcJobRun(run)
			if err != nil {
				status_code = WriteJsonRespHeader(w, http.StatusInternalServerError)
				je.Encode(JsonErrmsg{Text: err.Error()})
				goto oops
			}
			if stopped {
				status_code = WriteEmptyRespHeader(w, http.StatusAccepted)
			} else {
				status_code = WriteEmptyRespHeader(w, http.StatusNoContent)
			}

		default:
			status_code = WriteEmptyRespHeader(w, http.StatusMethodNotAllowed)
	}

	return status_code, nil

oops:
	return status_code, err
}
