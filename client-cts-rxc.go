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
import "unicode"

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

	cts.C.log.Write(cts.Sid, LOG_DEBUG, "Started rxc(%d) loop for %s(%s) - %s", crp.id, crp.req_type, crp.req_script, crp.cmd.String())

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

func split_simple_command(script string) ([]string, error) {
	var args []string
	var buf bytes.Buffer
	var ch rune
	var quote rune
	var token_started bool
	var escaped bool

	args = make([]string, 0)

	for _, ch = range script {
		if escaped {
			buf.WriteRune(ch)
			token_started = true
			escaped = false
			continue
		}

		if ch == '\\' {
			escaped = true
			token_started = true
			continue
		}

		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				buf.WriteRune(ch)
				token_started = true
			}
			continue
		}

		switch ch {
			case '\'', '"':
				quote = ch
				token_started = true

			default:
				if unicode.IsSpace(ch) {
					if token_started {
						args = append(args, buf.String())
						buf.Reset()
						token_started = false
					}
				} else {
					buf.WriteRune(ch)
					token_started = true
				}
		}
	}

	if escaped { return nil, fmt.Errorf("dangling escape in simple command") }
	if quote != 0 { return nil, fmt.Errorf("unterminated quote in simple command") }
	if token_started { args = append(args, buf.String()) }
	if len(args) <= 0 { return nil, fmt.Errorf("blank simple command") }

	return args, nil
}

func expand_rxc_profile_arg(arg_expr string, input_args []string) (string, error) {
	var buf bytes.Buffer
	var r rune
	var last_pos int
	var pos int
	var j int
	var arg_idx_str string
	var arg_idx int
	var arg_expr_len int
	var input_args_len int
	var err error

	input_args_len = len(input_args)
	arg_expr_len = len(arg_expr)
	last_pos = 0
	for pos, r = range arg_expr { // use the for .. range expression for rune-based traversal
		if r != '$' { continue }

		buf.WriteString(arg_expr[last_pos:pos])

		if pos + 1 >= arg_expr_len {
			// nothing after $
			buf.WriteByte('$')
			last_pos = pos + 1
			break
		}

		if arg_expr[pos + 1] == '$' {
			// convert $$ to a literal $
			buf.WriteByte('$')
			last_pos = pos + 2
			continue
		}

		if arg_expr[pos + 1] != '{' {
			// $ not followed by {
			buf.WriteByte('$')
			last_pos = pos + 1
			continue
		}

		j = pos + 2
		for j < arg_expr_len && arg_expr[j] != '}' { j++ }
		if j >= arg_expr_len {
			return "", fmt.Errorf("unterminated rxc profile args expression %s", arg_expr)
		}

		arg_idx_str = arg_expr[pos + 2:j]
		if arg_idx_str == "@" {
			return "", fmt.Errorf("invalid use of ${@} in rxc profile args expression %s", arg_expr)
		}

		arg_idx, err = strconv.Atoi(arg_idx_str)
		if err != nil || arg_idx <= 0 {
			return "", fmt.Errorf("invalid rxc profile argument index ${%s}", arg_idx_str)
		}
		if arg_idx > input_args_len {
			return "", fmt.Errorf("rxc profile argument index ${%d} out of range", arg_idx)
		}

		buf.WriteString(input_args[arg_idx - 1])
		last_pos = j + 1
	}

	if last_pos < arg_expr_len { buf.WriteString(arg_expr[last_pos:]) }

	return buf.String(), nil
}

func expand_rxc_profile_args(arg_exprs []string, input_args []string) ([]string, error) {
	var args []string
	var arg_expr string

	args = make([]string, 0)

	for _, arg_expr = range arg_exprs {
		if arg_expr == "${@}" {
			// ${@} must be used alone
			args = append(args, input_args...)
		} else {
			var expanded string
			var err error
			// expand_rxc_profile_arg rejects ${@} as it doesn't allow
			// to combine it with other elements (e.g. xx${@}yy is disallowed).
			expanded, err = expand_rxc_profile_arg(arg_expr, input_args)
			if err != nil { return nil, err }
			args = append(args, expanded)
		}
	}

	return args, nil
}

func apply_rxc_user(cmd *exec.Cmd, rxc_user string) error {
	var uid int
	var gid int
	var u *user.User
	var err error

	u, err = user.Lookup(rxc_user)
	if err != nil { return err }

	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
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
		"USER=" + u.Username,
	)

	return nil
}

func connect_cmd(c *Client, type_ string, script string) (*exec.Cmd, *os.File, *os.File, error) {
	var cmd *exec.Cmd
	var in io.WriteCloser
	var out io.ReadCloser
	var in_f *os.File
	var out_f *os.File
	var argv []string
	var cmd_argv []string
	var profile *ClientRxcProfile
	var effective_user string
	var ok bool
	var err error

	effective_user = c.rxc_user

/*	if type_ == "bash" {
		// TODO: refactor stop using Context
		//cmd = exec.CommandContext()
		cmd = exec.Command("/bin/bash", "-c", script)
	} else */if type_ == "simple" {
		argv, err = split_simple_command(script)
		if err != nil {
			return nil, nil, nil, err
		}
		if argv[0] == "" {
			return nil, nil, nil, fmt.Errorf("blank simple command")
		}
		profile, err = c.ResolveRxcProfile(argv[0])
		if err != nil {
			return nil, nil, nil, err
		}
		if profile == nil {
			return nil, nil, nil, fmt.Errorf("unknown rxc profile %s", argv[0])
		}
		if profile.User != "" { effective_user = profile.User }
		if profile.Args != nil {
			cmd_argv, err = expand_rxc_profile_args(profile.Args, argv[1:])
			if err != nil {
				return nil, nil, nil, err
			}
		} else {
			cmd_argv = argv[1:]
		}
		cmd = exec.Command(profile.Script, cmd_argv...)
	} else {
		return nil, nil, nil, fmt.Errorf("unsupported type - %s", type_)
	}

	if effective_user != "" {
		err = apply_rxc_user(cmd, effective_user)
		if err != nil {
			return nil, nil, nil, err
		}
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
	var err2 error

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

	crp = &ClientRxc{ cts: cts, id: id, req_type: args[0], req_script: args[1]  }
	err = unix.Pipe(crp.pfd[:])
	if err != nil {
		cts.rxc_mtx.Unlock()

		err2 = cts.psc.Send(MakeRxcStopPacket(id, err.Error()))
		if err2 != nil {
			cts.C.log.Write(cts.Sid, LOG_ERROR, "Failed to send %s for rxc(%d) start failure to server - %s", PACKET_KIND_RXC_STOP.String(), id, err2.Error())
		}
		return fmt.Errorf("unable to create rxc(%d) event fd for %s(%s) - %s", id, args[0], args[1], err.Error())
	}

	crp.cmd, crp.in, crp.out, err = connect_cmd(cts.C, args[0], args[1])
	if err != nil {
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
