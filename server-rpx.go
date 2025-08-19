package hodu

import "bufio"
import "bytes"
import "errors"
import "fmt"
import "io"
import "net"
import "net/http"
import "strconv"
import "strings"
import "sync"

type server_rpx struct {
	S *Server
	Id string
}

// ------------------------------------
func (rpx *server_rpx) Identity() string {
	return rpx.Id
}

func (rpx *server_rpx) Cors(req *http.Request) bool {
	return false
}

func (rpx *server_rpx) Authenticate(req *http.Request) (int, string) {
	return http.StatusOK, ""
}

func (rpx *server_rpx) get_client_token(req *http.Request) string {
	var val string

	// TODO: enhance this client token extraction logic with some expression language?
	val = req.Header.Get(rpx.S.Cfg.RpxClientTokenAttrName)
	if val == "" { val = req.Host }

	if rpx.S.Cfg.RpxClientTokenRegex != nil {
		val = get_regex_submatch(rpx.S.Cfg.RpxClientTokenRegex, val, rpx.S.Cfg.RpxClientTokenSubmatchIndex)
	}

	return val
}

func (rpx* server_rpx) handle_header_data(rpx_id uint64, data []byte, w http.ResponseWriter) (int, error) {
	var sc *bufio.Scanner
	var line string
	var flds []string
	var status_code int
	var err error

	sc = bufio.NewScanner(bytes.NewReader(data))
	sc.Scan()
	line = sc.Text()

	flds = strings.Fields(line)
	if (len(flds) < 2) { // i care about the status code..
		return http.StatusBadGateway, fmt.Errorf("invalid response status for rpx(%d) - %s", rpx_id, line)
	}
	status_code, err = strconv.Atoi(flds[1])
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("invalid response code for rpx(%d) - %s", rpx_id, err.Error())
	}

	for sc.Scan() {
		line = sc.Text()
		if line == "" { break }
		flds = strings.SplitN(line, ":", 2)
		if len(flds) == 2 {
			w.Header().Add(strings.TrimSpace(flds[0]), strings.TrimSpace(flds[1]))
		}
	}
	err = sc.Err()
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("failed to parse response for rpx(%d) - %s", rpx_id, err.Error())
	}

	w.WriteHeader(status_code)
	return status_code, nil
}

func (rpx *server_rpx) handle_response(srpx *ServerRpx, req *http.Request, w http.ResponseWriter, ws_upgrade bool, wg *sync.WaitGroup) {
	var start_resp []byte
	var status_code int
	var buf [4096]byte
	var n int
	var wr io.Writer
	var wrote_br_chan bool
	var err error

	defer wg.Done()

	select {
		case start_resp = <- srpx.start_chan:
			// received the header. ready to proceed to the body
			// do nothing. just continue
			status_code, err = rpx.handle_header_data(srpx.id, start_resp, w)
			if err != nil { goto done }

		case <- srpx.done_chan:
			err = fmt.Errorf("rpx(%d) terminated before receiving header", srpx.id)
			status_code = http.StatusBadGateway
			goto done
		case <- req.Context().Done():
			err = fmt.Errorf("rpx(%d) terminated before receiving header - %s", srpx.id, req.Context().Err().Error())
			status_code = http.StatusBadGateway
			goto done

		// no default. block
	}

	if ws_upgrade && status_code == http.StatusSwitchingProtocols {
		var hijk http.Hijacker
		var conn net.Conn
		var ok bool

		hijk, ok = w.(http.Hijacker)
		if !ok {
			err = fmt.Errorf("failed to upgrade rpx(%d) - not a hijacker", srpx.id)
			status_code = http.StatusInternalServerError
			goto done
		}

		conn, _, err = hijk.Hijack()
		if err != nil {
			err = fmt.Errorf("failed to upgrade rpx(%d) - %s", srpx.id, err.Error())
			status_code = http.StatusInternalServerError
			goto done
		}

		// websocket upgrade is successful
		srpx.br = conn
		srpx.br_chan <- true // inform another goroutine that the protocol switching is completed.
		wrote_br_chan = true

		wr = conn
	} else {
		if ws_upgrade {
			srpx.br_chan <- false
			wrote_br_chan = true
		} // indicate upgrade failure
		wr = w
	}

	for {
		n, err = srpx.pr.Read(buf[:])
		if n > 0 {
			var err2 error
			_, err2 = wr.Write(buf[:n])
			if err2 != nil {
				err = err2
				status_code = http.StatusInternalServerError
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			} else {
				status_code = http.StatusInternalServerError
			}
			break
		}
	}

done:
	// just send another in case the code got jump into this part for an error
	// may not be consumed but the channel is large enough for redundant data
	srpx.resp_status_code = status_code
	srpx.resp_error = err

	if ws_upgrade && !wrote_br_chan {
		srpx.br_chan <- false
	}
}

func (rpx *server_rpx) alloc_server_rpx(cts *ServerConn, req *http.Request) (*ServerRpx, error) {
	var srpx *ServerRpx
	var start_id uint64
	var assigned_id uint64
	var ok bool

	cts.rpx_mtx.Lock()
	start_id = cts.rpx_next_id
	for {
		_, ok = cts.rpx_map[cts.rpx_next_id]
		if !ok {
			assigned_id = cts.rpx_next_id
			cts.rpx_next_id++
			if cts.rpx_next_id == 0 { cts.rpx_next_id++ }
			break
		}
		cts.rpx_next_id++
		if cts.rpx_next_id == 0 { cts.rpx_next_id++ }
		if cts.rpx_next_id == start_id {
			// unlikely to happen but it cycled through the whole range.
			cts.rpx_mtx.Unlock()
			return nil, fmt.Errorf("failed to assign id")
		}
	}

	srpx = &ServerRpx{
		id: assigned_id,
		start_chan: make(chan []byte, 5),
		done_chan: make(chan bool, 5),
		br_chan: make(chan bool, 5),
	}
	srpx.br = req.Body
	srpx.pr, srpx.pw = io.Pipe()
	cts.rpx_map[assigned_id] = srpx

	cts.rpx_mtx.Unlock()
	cts.S.stats.rpx_sessions.Add(1)
	return srpx, nil
}

func (rpx *server_rpx) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var client_token string
	var start_sent bool
	var cts *ServerConn
	var status_code int
	var srpx *ServerRpx
	var ws_upgrade bool
	var buf [4096]byte
	var wg sync.WaitGroup
	var err error

	s = rpx.S
	client_token = rpx.get_client_token(req)
	cts = s.FindServerConnByClientToken(client_token)
	if cts == nil {
		status_code = WriteEmptyRespHeader(w, http.StatusNotFound)
		err = fmt.Errorf("unknown client token - %s", client_token)
		goto oops
	}

	srpx, err = rpx.alloc_server_rpx(cts, req)
	if err != nil {
		status_code = WriteEmptyRespHeader(w, http.StatusServiceUnavailable)
		err = fmt.Errorf("unable to allocate rpx - %s", err.Error())
		goto oops
	}

	// arrange to clear the rpx_map entry when this function exits
	defer func() {
		cts.rpx_mtx.Lock()
		delete(cts.rpx_map, srpx.id)
		cts.rpx_mtx.Unlock()
		cts.S.stats.rpx_sessions.Add(-1)
	}()

	ws_upgrade = strings.EqualFold(req.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade");
	if ws_upgrade && req.ContentLength > 0 {
		// while other webservers are ok with upgrade request with body payload,
		// this program rejects such a request for impelementation limitation as
		// it's not dealing with a raw byte but is using the standard web server handler.
		status_code = WriteEmptyRespHeader(w, http.StatusBadRequest)
		err = fmt.Errorf("failed to assign id")
		goto oops
	}

	err = cts.pss.Send(MakeRpxStartPacket(srpx.id, get_http_req_line_and_headers(req, true)))
	if err != nil {
		status_code = WriteEmptyRespHeader(w, http.StatusBadGateway)
		goto oops
	}
	start_sent = true

	wg.Add(1)
	go rpx.handle_response(srpx, req, w, ws_upgrade, &wg)

	if ws_upgrade {
		// wait until the protocol switching is done in rpx.handle_response()
		var upgraded bool
		upgraded = <- srpx.br_chan
		if upgraded {
			// arrange to close the hijacked connection inside rpx.handle_response()
			defer srpx.br.Close()
		}
	}

	for {
		var n int

		n, err = srpx.br.Read(buf[:])
		if n > 0 {
			var err2 error
			err2 = cts.pss.Send(MakeRpxDataPacket(srpx.id, buf[:n]))
			if err2 != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusBadGateway)
				goto oops
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = cts.pss.Send(MakeRpxEofPacket(srpx.id))
				if err != nil {
					status_code = WriteEmptyRespHeader(w, http.StatusBadGateway)
					goto oops
				}
				break
			}
			status_code = WriteEmptyRespHeader(w, http.StatusInternalServerError)
			goto oops
		}
	}

	wg.Wait()
	if srpx.resp_error != nil {
		status_code = WriteEmptyRespHeader(w, srpx.resp_status_code)
		err = srpx.resp_error
		goto oops
	}

	select {
		case <- srpx.done_chan:
			// anything to do?
		case <- req.Context().Done():
			// anything to do?
		// no default. block
	}

	cts.pss.Send(MakeRpxStopPacket(srpx.id))
	return srpx.resp_status_code, nil

oops:
	if srpx != nil && start_sent { cts.pss.Send(MakeRpxStopPacket(srpx.id)) }
	return status_code, err
}
