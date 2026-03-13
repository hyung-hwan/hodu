package hodu

import "errors"
import "fmt"
import "io"
import "strconv"
import "strings"
import "sync"

import pts "github.com/creack/pty"
import "golang.org/x/sys/unix"

// rpty
func (cts *ClientConn) FindClientRptyById(id uint64) *ClientRpty {
	var crp *ClientRpty
	var ok bool

	cts.rpty_mtx.Lock()
	crp, ok = cts.rpty_map[id]
	cts.rpty_mtx.Unlock()

	if !ok { crp = nil }
	return crp
}

func (cts *ClientConn) RptyLoop(crp *ClientRpty, wg *sync.WaitGroup) {

	var poll_fds []unix.PollFd
	var buf [2048]byte
	var n int
	var err error

	defer wg.Done()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Started rpty(%d) for %s(%s)", crp.id, cts.C.pty_shell, cts.C.pty_user)

	cts.C.stats.rpty_sessions.Add(1)

	poll_fds = []unix.PollFd{
		unix.PollFd{Fd: int32(crp.tty.Fd()), Events: unix.POLLIN},
		unix.PollFd{Fd: int32(crp.pfd[0]), Events: unix.POLLIN},
	}

	for {
		n, err = unix.Poll(poll_fds, -1) // -1 means wait indefinitely
		if err != nil {
			if errors.Is(err, unix.EINTR) { continue }
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to poll rpty(%d) stdout - %s", crp.id, err.Error())
			break
		}
		if n == 0 { // timed out
			continue
		}

		if (poll_fds[0].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rpty(%d) stdout", crp.id)
			break
		}
		if (poll_fds[1].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rpty(%d) event pipe", crp.id)
			break
		}

		if (poll_fds[0].Revents & unix.POLLIN) != 0 {
			n, err = crp.tty.Read(buf[:])
			if n > 0 {
				var err2 error
				err2 = cts.psc.Send(MakeRptyDataPacket(crp.id, buf[:n]))
				if err2 != nil {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s from rpty(%d) stdout to server - %s", PACKET_KIND_RPTY_DATA.String(), crp.id, err2.Error())
					break
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to read rpty(%d) stdout - %s", crp.id, err.Error())
				}
				break
			}
		}
		if (poll_fds[1].Revents & unix.POLLIN) != 0 {
			// don't care to read the pipe as it is closed after the loop
			//unix.Read(crp.pfd[0], )
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "Stop request noticed on rpty(%d) event pipe", crp.id)
			break
		}
	}

	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Ending rpty(%d) loop", crp.id)
	cts.psc.Send(MakeRptyStopPacket(crp.id, ""))

	crp.ReqStop()
	crp.cmd.Wait()
	crp.tty.Close()
	unix.Close(crp.pfd[0])
	unix.Close(crp.pfd[1])

	cts.rpty_mtx.Lock()
	delete(cts.rpty_map, crp.id)
	cts.rpty_mtx.Unlock()

	cts.C.stats.rpty_sessions.Add(-1)
	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Ended rpty(%d) loop", crp.id)
}

func (cts *ClientConn) StartRpty(id uint64, wg *sync.WaitGroup) error {
	var crp *ClientRpty
	var ok bool
	var i int
	var err error

	cts.rpty_mtx.Lock()
	_, ok = cts.rpty_map[id]
	if ok {
		cts.rpty_mtx.Unlock()
		return fmt.Errorf("multiple start on rpty id %d", id)
	}

	crp = &ClientRpty{ cts: cts, id: id }
	err = unix.Pipe(crp.pfd[:])
	if err != nil {
		cts.rpty_mtx.Unlock()
		cts.psc.Send(MakeRptyStopPacket(id, err.Error()))
		return fmt.Errorf("unable to create rpty(%d) event fd for %s(%s) - %s", id, cts.C.pty_shell, cts.C.pty_user, err.Error())
	}
	crp.cmd, crp.tty, err = connect_pty(cts.C.pty_shell, cts.C.pty_user)
	if err != nil {
		cts.rpty_mtx.Unlock()
		cts.psc.Send(MakeRptyStopPacket(id, err.Error()))
		unix.Close(crp.pfd[0])
		unix.Close(crp.pfd[1])
		return fmt.Errorf("unable to start rpty(%d) for %s(%s) - %s", id, cts.C.pty_shell, cts.C.pty_user, err.Error())
	}

	for i = 0; i < 2; i++ {
		var flags int
		flags, err = unix.FcntlInt(uintptr(crp.pfd[i]), unix.F_GETFL, 0)
		if err != nil {
			unix.FcntlInt(uintptr(crp.pfd[i]), unix.F_SETFL, flags | unix.O_NONBLOCK)
		}
	}

	cts.rpty_map[id] = crp

	wg.Add(1)
	go cts.RptyLoop(crp, wg)

	cts.rpty_mtx.Unlock()

	return nil
}

func (cts *ClientConn) StopRpty(id uint64) error {
	var crp *ClientRpty

	crp = cts.FindClientRptyById(id)
	if crp == nil {
		return fmt.Errorf("unknown rpty id %d", id)
	}

	crp.ReqStop()
	return nil
}

func (cts *ClientConn) WriteRpty(id uint64, data []byte) error {
	var crp *ClientRpty

	crp = cts.FindClientRptyById(id)
	if crp == nil {
		return fmt.Errorf("unknown rpty id %d", id)
	}

	crp.tty.Write(data)
	return nil
}

func (cts *ClientConn) WriteRptySize(id uint64, data []byte) error {
	var crp *ClientRpty
	var flds []string

	crp = cts.FindClientRptyById(id)
	if crp == nil {
		return fmt.Errorf("unknown rpty id %d", id)
	}

	flds = strings.Split(string(data), " ")
	if len(flds) == 2 {
		var rows int
		var cols int
		rows, _ = strconv.Atoi(flds[0])
		cols, _ = strconv.Atoi(flds[1])
		pts.Setsize(crp.tty, &pts.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	}
	return nil
}

func (cts *ClientConn) HandleRptyEvent(packet_type PACKET_KIND, evt *RptyEvent) error {

	switch packet_type {
		case PACKET_KIND_RPTY_START:
			return cts.StartRpty(evt.Id, &cts.C.wg)

		case PACKET_KIND_RPTY_STOP:
			return cts.StopRpty(evt.Id)

		case PACKET_KIND_RPTY_DATA:
			return cts.WriteRpty(evt.Id, evt.Data)

		case PACKET_KIND_RPTY_SIZE:
			return cts.WriteRptySize(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}
