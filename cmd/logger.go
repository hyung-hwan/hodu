package main

import "fmt"
import "hodu"
import "io"
import "os"
import "path/filepath"
import "runtime"
import "strings"
import "sync"
import "time"

type app_logger_msg_t struct {
	code int
	data string
}

type AppLogger struct {
	id              string
	out             io.Writer
	mask            hodu.LogMask

	file            *os.File
	file_name       string // you can get the file name from file but this is to preserve the original.
	file_rotate     int
	file_max_size   int64
	msg_chan        chan app_logger_msg_t
	wg              sync.WaitGroup
}

func NewAppLogger (id string, w io.Writer, mask hodu.LogMask) *AppLogger {
	var l *AppLogger
	l = &AppLogger{
		id: id,
		out: w,
		mask: mask,
		msg_chan: make(chan app_logger_msg_t, 256),
	}
	l.wg.Add(1)
	go l.logger_task()
	return l
}

func NewAppLoggerToFile (id string, file_name string, max_size int64, rotate int, mask hodu.LogMask) (*AppLogger, error) {
	var l *AppLogger
	var f *os.File
	var matched bool
	var err error

	f, err = os.OpenFile(file_name, os.O_CREATE | os.O_APPEND | os.O_WRONLY, 0666)
	if err != nil { return nil, err }

	if os.PathSeparator == '/' {
		// this check is performed only on systems where the path separator is /.
		matched, _ = filepath.Match("/dev/*", file_name)
		if matched {
			// if the log file is under /dev, disable rotation
			max_size = 0
			rotate = 0
		}
	}

	l = &AppLogger{
		id: id,
		out: f,
		mask: mask,
		file: f,
		file_name: file_name,
		file_max_size: max_size,
		file_rotate: rotate,
		msg_chan: make(chan app_logger_msg_t, 256),
	}
	l.wg.Add(1)
	go l.logger_task()
	return l, nil
}

func (l *AppLogger) Close() {
	l.msg_chan <- app_logger_msg_t{code: 1}
	l.wg.Wait()
	if l.file != nil { l.file.Close() }
}

func (l *AppLogger) Rotate() {
	l.msg_chan <- app_logger_msg_t{code: 2}
}

func (l *AppLogger) logger_task() {
	var msg app_logger_msg_t
	defer l.wg.Done()

main_loop:
	for {
		select {
			case msg = <-l.msg_chan:
				if msg.code == 0 {
					//l.out.Write([]byte(msg))
					io.WriteString(l.out, msg.data)
					if l.file_max_size > 0 && l.file != nil {
						var fi os.FileInfo
						var err error
						fi, err = l.file.Stat()
						if err == nil && fi.Size() >= l.file_max_size {
							l.rotate()
						}
					}
				} else if msg.code == 1 {
					break main_loop
				} else if msg.code == 2 {
					l.rotate()
				}
				// other code must not appear here.
		}
	}
}


func (l *AppLogger) Write(id string, level hodu.LogLevel, fmtstr string, args ...interface{}) {
	if l.mask & hodu.LogMask(level) == 0 { return }
	l.write(id, level, 1, fmtstr, args...)
}

func (l *AppLogger) WriteWithCallDepth(id string, level hodu.LogLevel, call_depth int, fmtstr string, args ...interface{}) {
	if l.mask & hodu.LogMask(level) == 0 { return }
	l.write(id, level, call_depth + 1, fmtstr, args...)
}

func (l *AppLogger) write(id string, level hodu.LogLevel, call_depth int, fmtstr string, args ...interface{}) {
	var now time.Time
	var off_m int
	var off_h int
	var off_s int
	var msg string
	var callerfile string
	var caller_line int
	var caller_ok bool
	var sb strings.Builder

	//if l.mask & hodu.LogMask(level) == 0 { return }

	now = time.Now()

	_, off_s = now.Zone()
	off_m = off_s / 60;
	off_h = off_m / 60;
	off_m = off_m % 60;
	if (off_m < 0) { off_m = -off_m; }

	sb.WriteString(
		fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d %+03d%02d ",
		now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), off_h, off_m))

	_, callerfile, caller_line, caller_ok = runtime.Caller(1 + call_depth)

	if caller_ok {
		sb.WriteString(fmt.Sprintf("[%s:%d] ", filepath.Base(callerfile), caller_line))
	}
	sb.WriteString(l.id)
	if id != "" {
		sb.WriteString("(")
		sb.WriteString(id)
		sb.WriteString(")")
	}
	sb.WriteString(": ")
	msg = fmt.Sprintf(fmtstr, args...)
	sb.WriteString(msg)
	if msg[len(msg) - 1] != '\n' { sb.WriteRune('\n') }

	// use queue to avoid blocking operation as much as possible
	l.msg_chan <- app_logger_msg_t{ code: 0, data: sb.String() }
}

func (l *AppLogger) rotate() {
	var f *os.File
	var fi os.FileInfo
	var i int
	var last_rot_no int
	var err error

	if l.file == nil { return }
	if l.file_rotate <= 0 { return }

	fi, err = l.file.Stat()
	if err == nil && fi.Size() <= 0 { return }

	for i = l.file_rotate - 1; i > 0; i-- {
		if os.Rename(fmt.Sprintf("%s.%d", l.file_name, i), fmt.Sprintf("%s.%d", l.file_name, i + 1)) == nil {
			if last_rot_no == 0 { last_rot_no = i + 1 }
		}
	}
	if os.Rename(l.file_name, fmt.Sprintf("%s.%d", l.file_name, 1)) == nil {
		if last_rot_no == 0 { last_rot_no = 1 }
	}

	f, err = os.OpenFile(l.file_name, os.O_CREATE | os.O_TRUNC | os.O_APPEND | os.O_WRONLY, 0666)
	if err != nil {
		l.file.Close()
		l.file = nil
		l.out = os.Stderr
		// don't reset l.file_name. you can derive that there was an error
		// if l.file_name is not blank, and if l.out is os.Stderr,
	} else {
		l.file.Close()
		l.file = f
		l.out = l.file
	}
}
