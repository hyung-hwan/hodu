package hodu

import "bytes"
import "encoding/gob"
import "errors"
import "fmt"
import "io"
import "os"
import "os/exec"
import "sync"

import "golang.org/x/sys/unix"

func (cts *ClientConn) FindClientRxcById(id uint64) *ClientRxc {
	var crp *ClientRxc
	var ok bool

	cts.rxc_mtx.Lock()
	crp, ok = cts.rxc_map[id]
	cts.rxc_mtx.Unlock()

	if !ok { crp = nil }
	return crp
}

func (cts *ClientConn) RxcLoop(crp *ClientRxc, wg *sync.WaitGroup) {

	var poll_fds []unix.PollFd
	var buf [2048]byte
	var n int
	var err error

	defer wg.Done()

	cts.C.log.Write(cts.Sid, LOG_INFO, "Started rxc(%d) for %s(%s)", crp.id, cts.C.pty_shell, cts.C.pty_user)

	cts.C.stats.rxc_sessions.Add(1)

	poll_fds = []unix.PollFd{
		unix.PollFd{Fd: int32(crp.tty.Fd()), Events: unix.POLLIN},
		unix.PollFd{Fd: int32(crp.pfd[0]), Events: unix.POLLIN},
	}

	for {
		n, err = unix.Poll(poll_fds, -1) // -1 means wait indefinitely
		if err != nil {
			if errors.Is(err, unix.EINTR) { continue }
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to poll rxc(%d) stdout - %s", crp.id, err.Error())
			break
		}
		if n == 0 { // timed out
			continue
		}

		if (poll_fds[0].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rxc(%d) stdout", crp.id)
			break
		}
		if (poll_fds[1].Revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rxc(%d) event pipe", crp.id)
			break
		}

		if (poll_fds[0].Revents & unix.POLLIN) != 0 {
			n, err = crp.tty.Read(buf[:])
			if n > 0 {
				var err2 error
				err2 = cts.psc.Send(MakeRxcDataPacket(crp.id, buf[:n]))
				if err2 != nil {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s from rxc(%d) stdout to server - %s", PACKET_KIND_RXC_DATA.String(), crp.id, err2.Error())
					break
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to read rxc(%d) stdout - %s", crp.id, err.Error())
				}
				break
			}
		}
		if (poll_fds[1].Revents & unix.POLLIN) != 0 {
			// don't care to read the pipe as it is closed after the loop
			//unix.Read(crp.pfd[0], )
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "Stop request noticed on rxc(%d) event pipe", crp.id)
			break
		}
	}

	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Ending rxc(%d) loop", crp.id)
	err = cts.psc.Send(MakeRxcStopPacket(crp.id, ""))
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s from rxc(%d) to server - %s", PACKET_KIND_RXC_STOP.String(), crp.id, err.Error())
	}

	crp.ReqStop()
	crp.cmd.Wait()
	crp.tty.Close()
	unix.Close(crp.pfd[0])
	unix.Close(crp.pfd[1])

	cts.rxc_mtx.Lock()
	delete(cts.rxc_map, crp.id)
	cts.rxc_mtx.Unlock()

	cts.C.stats.rxc_sessions.Add(-1)
	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Ended rxc(%d) loop", crp.id)
}

func connect_cmd(_type string, script string) (*exec.Cmd, *os.File, error) {
	var cmd *exec.Cmd
	var out io.ReadCloser
	var out_f *os.File
	var ok bool
	var err error

	if _type == "bash" {
		// TODO: refactor stop using Context
		//cmd = exec.CommandContext()
		cmd = exec.Command("/bin/bash", "-c", script)
	} else {
		return nil, nil, fmt.Errorf("unsupported type - %s", _type)
	}

	out, err = cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get stdout pipe - %s", err.Error())
	}
	out_f, ok = out.(*os.File)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported stdout pipe")
	}

	err = cmd.Start()
	if err != nil {
		return nil, nil, err
	}

	return cmd, out_f, nil
}

func (cts *ClientConn) StartRxc(id uint64, data []byte, wg *sync.WaitGroup) error {
	var crp *ClientRxc
	var ok bool
	var i int
	var dec *gob.Decoder
	var args []string
	var err error

	dec = gob.NewDecoder(bytes.NewBuffer(data));
	err = dec.Decode(&args);
	if err != nil {
		return fmt.Errorf("unable to decode data for start on rxc(%d) - %s", id, err.Error())
	}
	if len(args) != 2 {
		return fmt.Errorf("invalid data for start on rxc(%d)", id)
	}

	cts.rxc_mtx.Lock()
	_, ok = cts.rxc_map[id]
	if ok {
		cts.rxc_mtx.Unlock()
		return fmt.Errorf("multiple start on rxc(%d)", id)
	}

	crp = &ClientRxc{ cts: cts, id: id }
	err = unix.Pipe(crp.pfd[:])
	if err != nil {
		var err2 error

		cts.rxc_mtx.Unlock()
		err2 = cts.psc.Send(MakeRxcStopPacket(id, err.Error()))
		if err2 != nil {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s for rxc(%d) start failure to server - %s", PACKET_KIND_RXC_STOP.String(), id, err2.Error())
		}
		//return fmt.Errorf("unable to create rxc(%d) event fd for %s(%s) - %s", id, cts.C.pty_shell, cts.C.pty_user, err.Error())
		return fmt.Errorf("unable to create rxc(%d) event fd for %s(%s) - %s", id, args[0], args[1], err.Error())
	}
	//crp.cmd, crp.tty, err = connect_pty(cts.C.pty_shell, cts.C.pty_user)
	crp.cmd, crp.tty, err = connect_cmd(args[0], args[1])
	if err != nil {
		var err2 error

		cts.rxc_mtx.Unlock()
		err2 = cts.psc.Send(MakeRxcStopPacket(id, err.Error()))
		if err2 != nil {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s for rxc(%d) start failure to server - %s", PACKET_KIND_RXC_STOP.String(), id, err2.Error())
		}
		unix.Close(crp.pfd[0])
		unix.Close(crp.pfd[1])
		return fmt.Errorf("unable to start rxc(%d) for %s(%s) - %s", id, cts.C.pty_shell, cts.C.pty_user, err.Error())
	}

	for i = 0; i < 2; i++ {
		var flags int
		flags, err = unix.FcntlInt(uintptr(crp.pfd[i]), unix.F_GETFL, 0)
		if err != nil {
			unix.FcntlInt(uintptr(crp.pfd[i]), unix.F_SETFL, flags | unix.O_NONBLOCK)
		}
	}

	cts.rxc_map[id] = crp

	wg.Add(1)
	go cts.RxcLoop(crp, wg)

	cts.rxc_mtx.Unlock()

	return nil
}

func (cts *ClientConn) StopRxc(id uint64) error {
	var crp *ClientRxc

	crp = cts.FindClientRxcById(id)
	if crp == nil {
		return fmt.Errorf("unknown rxc id %d", id)
	}

	crp.ReqStop()
	return nil
}

func (cts *ClientConn) WriteRxc(id uint64, data []byte) error {
	var crp *ClientRxc

	crp = cts.FindClientRxcById(id)
	if crp == nil {
		return fmt.Errorf("unknown rxc id %d", id)
	}

	crp.tty.Write(data)
	return nil
}

func (cts *ClientConn) HandleRxcEvent(packet_type PACKET_KIND, evt *RxcEvent) error {

	switch packet_type {
		case PACKET_KIND_RXC_START:
			return cts.StartRxc(evt.Id, evt.Data, &cts.C.wg)

		case PACKET_KIND_RXC_STOP:
			return cts.StopRxc(evt.Id)

		case PACKET_KIND_RXC_DATA:
			return cts.WriteRxc(evt.Id, evt.Data)
	}

	// ignore other packet types
	return nil
}
