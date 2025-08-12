package hodu

import "encoding/json"
import "fmt"
import "os"
import "os/exec"
import "os/user"
import "strconv"
import "syscall"

import pts "github.com/creack/pty"
import "golang.org/x/net/websocket"
import "golang.org/x/sys/unix"

func connect_pty(pty_shell string, pty_user string) (*exec.Cmd, *os.File, error) {
	var cmd *exec.Cmd
	var tty *os.File
	var err error

	if pty_shell == "" {
		return nil, nil, fmt.Errorf("blank pty shell")
	}

	cmd = exec.Command(pty_shell)
	if pty_user != "" {
		var uid int
		var gid int
		var u *user.User

		u, err = user.Lookup(pty_user)
		if err != nil { return nil, nil, err }

		uid, _ = strconv.Atoi(u.Uid)
		gid, _ = strconv.Atoi(u.Gid)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uint32(uid),
				Gid: uint32(gid),
			},
			Setsid:  true,
		}
		cmd.Dir = u.HomeDir
		cmd.Env = append(cmd.Env,
			"HOME=" + u.HomeDir,
			"LOGNAME=" + u.Username,
			"PATH=" + os.Getenv("PATH"),
			"SHELL=" + pty_shell,
			"TERM=xterm",
			"USER=" + u.Username,
		)
	}

	tty, err = pts.Start(cmd)
	if err != nil {
		return nil, nil, err
	}

	//syscall.SetNonblock(int(tty.Fd()), true)
	unix.SetNonblock(int(tty.Fd()), true)

	return cmd, tty, nil
}

func send_ws_data_for_xterm(ws *websocket.Conn, type_val string, data string) error {
	var msg []byte
	var err error
	msg, err = json.Marshal(json_xterm_ws_event{Type: type_val, Data: []string{ data } })
	if err == nil { err = websocket.Message.Send(ws, msg) }
	return err
}

