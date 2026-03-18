package hodu

import "bufio"
import "bytes"
import "context"
import "crypto/tls"
import "errors"
import "fmt"
import "io"
import "net"
import "net/http"  
import "sync"
import "strings"
import "time"

// rpx
func (cts *ClientConn) FindClientRpxById(id uint64) *ClientRpx {
	var crpx *ClientRpx
	var ok bool

	cts.rpx_mtx.Lock()
	crpx, ok = cts.rpx_map[id]
	cts.rpx_mtx.Unlock()

	if !ok { crpx = nil }
	return crpx
}

func (cts *ClientConn) server_pipe_to_ws_target(crpx* ClientRpx, conn net.Conn, wg *sync.WaitGroup) {
	var buf [4096]byte
	var n int
	var err error

	defer wg.Done()

	for {
		n, err = crpx.pr.Read(buf[:])
		if n > 0 {
			var err2 error
			_, err2 = conn.Write(buf[:n])
			if err2 != nil {
				cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to write websocket for rpx(%d) - %s", crpx.id, err2.Error())
				break
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) { break }
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to read pipe for rpx(%d) - %s", crpx.id, err.Error())
			break
		}
	}
}

func (cts *ClientConn) proxy_ws(crpx *ClientRpx, raw_req []byte, req *http.Request) (int, error) {
	var l_wg sync.WaitGroup
	var conn net.Conn
	var resp *http.Response
	var r *bufio.Reader
	var buf [4096]byte
	var n int
	var err error

	if cts.C.rpx_target_tls != nil {
		var dialer *tls.Dialer
		dialer = &tls.Dialer{
			NetDialer: &net.Dialer{},
			Config: cts.C.rpx_target_tls,
		}
		conn, err = dialer.DialContext(crpx.ctx, "tcp", cts.C.rpx_target_addr) // TODO: no hard coding
	} else {
		var dialer *net.Dialer
		dialer = &net.Dialer{}
		conn, err = dialer.DialContext(crpx.ctx, "tcp", cts.C.rpx_target_addr) // TODO: no hard coding
	}
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to dial websocket for rpx(%d) - %s", crpx.id, err.Error())
	}
	defer conn.Close()

	// TODO: make this atomic?
	crpx.ws_conn = conn

	// write the raw request line and headers as sent by the server.
	// for the upgrade request, i assume no payload.
	_, err = conn.Write(raw_req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to write websocket request for rpx(%d) - %s", crpx.id, err.Error())
	}

	r = bufio.NewReader(conn)
	resp, err = http.ReadResponse(r, req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to write websocket response for rpx(%d) - %s", crpx.id, err.Error())
	}
	defer resp.Body.Close()

	err = cts.psc.Send(MakeRpxStartPacket(crpx.id, get_http_resp_line_and_headers(resp)))
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to send rpx(%d) WebSocket headers to server - %s", crpx.id, err.Error())
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		// websock upgrade failed. let the code jump to the done
		// label to skip reading from the pipe. the server side
		// has the code to ensure no content-length. and the upgrade
		// fails, the pipe below will be pending forever as the server
		// side doesn't send data and there's no feeding to the pipe.
		return resp.StatusCode, fmt.Errorf("protocol switching failed for rpx(%d)", crpx.id)
	}

	// unlike with the normal request, the actual pipe is not read
	// until the initial switching protocol response is received.

	l_wg.Add(1)
	go cts.server_pipe_to_ws_target(crpx, conn, &l_wg)

	for {
		n, err = conn.Read(buf[:])
		if n > 0 {
			var err2 error
			err2 = cts.psc.Send(MakeRpxDataPacket(crpx.id, buf[:n]))
			if err2 != nil {
				crpx.ReqStop() // to break server_pipe_ws_target. don't care about multiple stops
				return resp.StatusCode, fmt.Errorf("failed to send rpx(%d) data to server - %s", crpx.id, err2.Error())
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				cts.psc.Send(MakeRpxEofPacket(crpx.id))
				cts.C.log.Write(cts.Sid, LOG_DEBUG, "WebSocket rpx(%d) closed by server", crpx.id)
				break
			}

			crpx.ReqStop() // to break server_pipe_ws_target. don't care about multiple stops
			return resp.StatusCode, fmt.Errorf("failed to read WebSocket rpx(%d) - %s", crpx.id, err.Error())
		}
	}

	// wait until the pipe reading(from the server side) goroutine is over
	l_wg.Wait()
	return resp.StatusCode, nil
}

func (cts *ClientConn) proxy_http(crpx *ClientRpx, req *http.Request) (int, error) {
	var tr *http.Transport
	var resp *http.Response
	var buf [4096]byte
	var n int
	var err error

	tr = &http.Transport {
		DisableKeepAlives: true, // this implementation can't support keepalive..
	}
	if cts.C.rpx_target_tls != nil {
		tr.TLSClientConfig = cts.C.rpx_target_tls
	}

	resp, err = tr.RoundTrip(req)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to send rpx(%d) request - %s", crpx.id, err.Error())
	}
	defer resp.Body.Close()

	err = cts.psc.Send(MakeRpxStartPacket(crpx.id, get_http_resp_line_and_headers(resp)))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("failed to send rpx(%d) status and headers to server - %s", crpx.id, err.Error())
	}

	for {
		n, err = resp.Body.Read(buf[:])
		if n > 0 {
			var err2 error
			err2 = cts.psc.Send(MakeRpxDataPacket(crpx.id, buf[:n]))
			if err2 != nil {
				return resp.StatusCode, fmt.Errorf("failed to send rpx(%d) data to server - %s", crpx.id, err2.Error())
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return resp.StatusCode, fmt.Errorf("failed to read response body for rpx(%d) - %s", crpx.id, err.Error())
		}
	}

	return resp.StatusCode, nil
}

func (cts *ClientConn) RpxLoop(crpx *ClientRpx, data []byte, wg *sync.WaitGroup) {
	var start_time time.Time
	var time_taken time.Duration
	var r *bufio.Reader
	var line string
	var flds []string
	var req_meth string
	var req_path string
	//var req_proto string
	var x_forwarded_host string
	var raw_req bytes.Buffer
	var status_code int
	var req *http.Request
	var err error

	defer wg.Done()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Starting rpx(%d) loop", crpx.id)

	start_time = time.Now()

	const rpx_header_line_max = 65535 // TODO: make this configurable

	r = bufio.NewReader(bytes.NewReader(data))
	line, err = read_line_limited(r, rpx_header_line_max)
	if err != nil && !errors.Is(err, io.EOF) {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "failed to parse request for rpx(%d) - %s", crpx.id, err.Error())
		goto done
	}
	line = strings.TrimRight(line, "\r\n")

	flds = strings.Fields(line)
	if len(flds) < 3 {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Invalid request line for rpx(%d) - %s", crpx.id, line)
		goto done
	}

// TODO: handle trailers...
	req_meth = flds[0]
	req_path = flds[1]
	//req_proto = flds[2]

	raw_req.WriteString(line)
	raw_req.WriteString("\r\n")

	// create a request assuming it's a normal http request
	req, err = http.NewRequestWithContext(crpx.ctx, req_meth, cts.C.rpx_target_url + req_path, crpx.pr)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "failed to create request for rpx(%d) - %s", crpx.id, err.Error())
		goto done
	}

	for {
		line, err = read_line_limited(r, rpx_header_line_max)
		if err != nil && !errors.Is(err, io.EOF) {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "failed to parse request for rpx(%d) - %s", crpx.id, err.Error())
			goto done
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { break }
		flds = strings.SplitN(line, ":", 2)
		if len(flds) == 2 {
			var k string
			var v string
			k = strings.TrimSpace(flds[0])
			v = strings.TrimSpace(flds[1])
			req.Header.Add(k, v)

			if strings.EqualFold(k, "Host") {
				// a normal http client would set HOst to be the target address.
				// the raw header is coming from the server. so it's different
				// from the host it's supposed to be. correct it to the right value.
				fmt.Fprintf(&raw_req, "%s: %s\r\n", k, req.Host)
			} else {
				raw_req.WriteString(line)
				raw_req.WriteString("\r\n")

				if strings.EqualFold(k, "X-Forwarded-Host") {
					x_forwarded_host = v
				}
			}
		}
		if errors.Is(err, io.EOF) { break }
	}
	raw_req.WriteString("\r\n")
	if x_forwarded_host == "" {
		x_forwarded_host = req.Host
	}

	if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(req.Header.Get("Connection")), "upgrade") {
		// websocket
		status_code, err = cts.proxy_ws(crpx, raw_req.Bytes(), req)
	} else {
		// normal http
		status_code, err = cts.proxy_http(crpx, req)
	}

	time_taken = time.Since(start_time)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "rpx(%d) %s - %s %s %d %.9f - failed to proxy - %s", crpx.id, x_forwarded_host, req_meth, req_path, status_code, time_taken.Seconds(), err.Error())
		goto done
	} else {
		cts.C.log.Write(cts.Sid, LOG_INFO, "rpx(%d) %s - %s %s %d %.9f", crpx.id, x_forwarded_host, req_meth, req_path, status_code, time_taken.Seconds())
	}

done:
	err = cts.psc.Send(MakeRpxStopPacket(crpx.id))
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "rpx(%d) Failed to send %s to server - %s", crpx.id, PACKET_KIND_RPX_STOP.String(), err.Error())
	}
	cts.C.log.Write(cts.Sid, LOG_INFO, "Ending rpx(%d) loop", crpx.id)

	crpx.ReqStop()

	cts.rpx_mtx.Lock()
	delete(cts.rpx_map, crpx.id)
	cts.rpx_mtx.Unlock()
	cts.C.stats.rpx_sessions.Add(-1)
	cts.C.log.Write(cts.Sid, LOG_INFO, "Ended rpx(%d) loop", crpx.id)
}

func (cts *ClientConn) StartRpx(id uint64, data []byte, wg *sync.WaitGroup) error {
	var crpx *ClientRpx
	var ok bool

	cts.rpx_mtx.Lock()
	_, ok = cts.rpx_map[id]
	if ok {
		cts.rpx_mtx.Unlock()
		return fmt.Errorf("multiple start on rpx id %d", id)
	}
	crpx = &ClientRpx{ id: id }
	cts.rpx_map[id] = crpx

	// i want the pipe to be created before the goroutine is started
	// so that the WriteRpx() can write to the pipe. i protect pipe creation
	// and context creation with a mutex
	crpx.pr, crpx.pw = io.Pipe()
	crpx.ctx, crpx.cancel = context.WithCancel(cts.C.Ctx)

	cts.rpx_mtx.Unlock()
	cts.C.stats.rpx_sessions.Add(1)

	wg.Add(1)
	go cts.RpxLoop(crpx, data, wg)

	return nil
}

func (cts *ClientConn) StopRpx(id uint64) error {
	var crpx *ClientRpx

	crpx = cts.FindClientRpxById(id)
	if crpx == nil {
		return fmt.Errorf("unknown rpx id %d", id)
	}

	crpx.ReqStop()
	return nil
}

func (cts *ClientConn) WriteRpx(id uint64, data []byte) error {
	var crpx *ClientRpx
	var err error

	crpx = cts.FindClientRpxById(id)
	if crpx == nil {
		return fmt.Errorf("unknown rpx id %d", id)
	}

// TODO: may have to write it in a goroutine to avoid blocking?
	_, err = crpx.pw.Write(data)
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to write rpx(%d) data - %s", id, err.Error())
		return err
	}
	return nil
}

func (cts *ClientConn) EofRpx(id uint64, data []byte) error {
	var crpx *ClientRpx

	crpx = cts.FindClientRpxById(id)
	if crpx == nil {
		return fmt.Errorf("unknown rpx id %d", id)
	}

	// close the writing end only. leave the reading end untouched
	crpx.pw.Close()

	return nil
}

func (cts *ClientConn) HandleRpxEvent(packet_type PACKET_KIND, evt *RpxEvent) error {
	switch packet_type {
		case PACKET_KIND_RPX_START:
			return cts.StartRpx(evt.Id, evt.Data, &cts.C.wg)

		case PACKET_KIND_RPX_STOP:
			return cts.StopRpx(evt.Id)

		case PACKET_KIND_RPX_DATA:
			return cts.WriteRpx(evt.Id, evt.Data)

		case PACKET_KIND_RPX_EOF:
			return cts.EofRpx(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}
