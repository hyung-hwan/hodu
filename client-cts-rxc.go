package hodu

import "bytes"
import "encoding/gob"
import "errors"
import "fmt"
import "io"
import "os"
import "os/exec"
import "os/user"
import "strconv"
import "sync"
import "syscall"

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
	var out_revents int16
	var sig_revents int16
	var err error

	defer wg.Done()

	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Started rxc(%d) loop for %s", crp.id, crp.cmd.String())

	cts.C.stats.rxc_sessions.Add(1)

	poll_fds = []unix.PollFd{
		unix.PollFd{Fd: int32(crp.out.Fd()), Events: unix.POLLIN},
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

		out_revents = poll_fds[0].Revents
		sig_revents = poll_fds[1].Revents

		if (out_revents & unix.POLLIN) != 0 {
			n, err = crp.out.Read(buf[:])
			if n > 0 {
				var err2 error
				err2 = cts.psc.Send(MakeRxcDataPacket(crp.id, buf[:n]))
				if err2 != nil {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s from rxc(%d) stdout to server - %s", PACKET_KIND_RXC_DATA.String(), crp.id, err2.Error())
					break
				} //else {
				//	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Sent %s from rxc(%d) stdout to server - %v", PACKET_KIND_RXC_DATA.String(), crp.id, buf[:n])
				//}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to read rxc(%d) stdout - %s", crp.id, err.Error())
				}
				break
			}
		}
		if (sig_revents & unix.POLLIN) != 0 {
			// don't care to read the pipe as it is closed after the loop
			//unix.Read(crp.pfd[0], )
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "Stop request noticed on rxc(%d) signal pipe", crp.id)
			break
		}
		if (out_revents & (unix.POLLERR | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "Error detected on rxc(%d) stdout", crp.id)
			break
		}
		if (sig_revents & (unix.POLLERR | unix.POLLHUP | unix.POLLNVAL)) != 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rxc(%d) signal pipe", crp.id)
			break
		}
		if (out_revents & unix.POLLHUP) != 0 && (out_revents & unix.POLLIN) == 0 {
			cts.C.log.Write(cts.Sid, LOG_DEBUG, "EOF detected on rxc(%d) stdout", crp.id)
			break
		}
	}

	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Ending rxc(%d) loop", crp.id)
	err = cts.psc.Send(MakeRxcStopPacket(crp.id, ""))
	if err != nil {
		cts.C.log.Write(cts.Sid, LOG_WARN, "Failed to send %s from rxc(%d) to server - %s", PACKET_KIND_RXC_STOP.String(), crp.id, err.Error())
	}

	crp.ReqStop()
	crp.in.Close() // close the input before waiting for program termination
	crp.cmd.Wait()
	crp.out.Close()
	unix.Close(crp.pfd[0])
	unix.Close(crp.pfd[1])

	cts.rxc_mtx.Lock()
	delete(cts.rxc_map, crp.id)
	cts.rxc_mtx.Unlock()

	cts.C.stats.rxc_sessions.Add(-1)
	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Ended rxc(%d) loop", crp.id)
}

func connect_cmd(rxc_user string, _type string, script string) (*exec.Cmd, *os.File, *os.File, error) {
	var cmd *exec.Cmd
	var in io.WriteCloser
	var out io.ReadCloser
	var in_f *os.File
	var out_f *os.File
	var ok bool
	var err error

	if _type == "bash" {
		// TODO: refactor stop using Context
		//cmd = exec.CommandContext()
		cmd = exec.Command("/bin/bash", "-c", script)

		if rxc_user != "" {
			var uid int
			var gid int
			var u *user.User

			u, err = user.Lookup(rxc_user)
			if err != nil { return nil, nil, nil, err }

			uid, _ = strconv.Atoi(u.Uid)
			gid, _ = strconv.Atoi(u.Gid)
			//if u.Uid != os.Geteuid() {
				cmd.SysProcAttr = &syscall.SysProcAttr{
					Credential: &syscall.Credential{
						Uid: uint32(uid),
						Gid: uint32(gid),
					},
					Setsid: true,
				}
				cmd.Dir = u.HomeDir
				cmd.Env = append(cmd.Env,
					"HOME=" + u.HomeDir,
					"LOGNAME=" + u.Username,
					"PATH=" + os.Getenv("PATH"),
					//"SHELL=" + pty_shell,
					//"TERM=xterm",
					"USER=" + u.Username,
				)
			//}
		}
	} else {
		return nil, nil, nil, fmt.Errorf("unsupported type - %s", _type)
	}

	out, err = cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to get stdout pipe - %s", err.Error())
	}
	out_f, ok = out.(*os.File)
	if !ok {
		out.Close()
		return nil, nil, nil, fmt.Errorf("unsupported stdout pipe")
	}

	in, err = cmd.StdinPipe()
	if err != nil {
		out.Close()
		return nil, nil, nil, fmt.Errorf("unable to get stdin pipe - %s", err.Error())
	}
	in_f, ok = in.(*os.File)
	if !ok {
		in.Close()
		out.Close()
		return nil, nil, nil, fmt.Errorf("unsupported stdin pipe")
	}

	err = cmd.Start()
	if err != nil {
		in.Close()
		out.Close()
		return nil, nil, nil, err
	}

	return cmd, in_f, out_f, nil
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
		return fmt.Errorf("unable to create rxc(%d) event fd for %s(%s) - %s", id, args[0], args[1], err.Error())
	}

	crp.cmd, crp.in, crp.out, err = connect_cmd(cts.C.rxc_user, args[0], args[1])
	if err != nil {
		var err2 error

		cts.rxc_mtx.Unlock()
		err2 = cts.psc.Send(MakeRxcStopPacket(id, err.Error()))
		if err2 != nil {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s for rxc(%d) start failure to server - %s", PACKET_KIND_RXC_STOP.String(), id, err2.Error())
		}
		unix.Close(crp.pfd[0])
		unix.Close(crp.pfd[1])
		return fmt.Errorf("unable to start rxc(%d) for %s(%s) - %s", id, args[0], args[1], err.Error())
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

	crp.in.Write(data)
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
