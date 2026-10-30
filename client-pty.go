package hodu

import "context"
import "fmt"
import "net/http"
import "strings"
import "text/template"

import "golang.org/x/net/websocket"

type client_pty_ws struct {
	C *Client
	Id string
	ws *websocket.Conn
}

type client_pty_xterm_file struct {
	client_ctl
	file string
}

// ------------------------------------------------------

func (pty *client_pty_ws) Identity() string {
	return pty.Id
}

func (pty *client_pty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	if pty.C.ctl_auth != nil {
		var status int
		status, _ = pty.C.ctl_auth.Authenticate(ws.Request(), "access-token")
		if status != http.StatusOK { ws.Close(); return status, fmt.Errorf("failed to authenticate") }
	}
	return serve_xterm_sessions(ws, func(ctx context.Context, data []string) (*xterm_backend, error) {
		return open_xterm_pty(ctx, pty.C.pty_shell, pty.C.pty_user, func(delta int64) { pty.C.stats.pty_sessions.Add(delta) })
	})
}

func (pty *client_pty_xterm_file) Authenticate(req *http.Request) (int, string) {
	if pty.c.ctl_auth != nil && pty.file == "xterm.html" {
		// this is not a real api call. but at least for xterm.html,
		// i don't bypass authentication and and in addition,
		// i check the value of the "access-token" parameter for
		// jwt authentication if it exists
		return pty.c.ctl_auth.Authenticate(req, "access-token")
	}

	// you can download other files without authentication
	// even if authentication is enabled. but this part must not be reached
	// because xterm.html is the only meanful resource as of now
	return http.StatusOK, ""
}

func (pty *client_pty_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var c *Client
	var status_code int
	var err error

	c = pty.c

	switch pty.file {
	/*
		case "xterm.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_js)
		case "xterm-addon-fit.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_addon_fit_js)
		case "xterm-addon-unicode11.js":
			status_code = WriteJsRespHeader(w, http.StatusOK)
			w.Write(xterm_addon_unicode11_js)
		case "xterm.css":
			status_code = WriteCssRespHeader(w, http.StatusOK)
			w.Write(xterm_css)
	*/
		case "xterm.html":
			var tmpl *template.Template

			tmpl = template.New("").Delims("{{@@", "@@}}")
			if c.xterm_html !=  "" {
				_, err = tmpl.Parse(c.xterm_html)
			} else {
				_, err = tmpl.Parse(xterm_html)
			}
			if err != nil {
				status_code = WriteEmptyRespHeader(w, http.StatusInternalServerError)
				goto oops
			} else {
				status_code = WriteHtmlRespHeader(w, http.StatusOK)
				tmpl.Execute(w,
					&xterm_session_info{
						Mode: "pty",
						ConnId: "-1",
						RouteId: "-1",
					})
			}


		case "_forbidden":
			status_code = WriteEmptyRespHeader(w, http.StatusForbidden)

		case "_notfound":
			status_code = WriteEmptyRespHeader(w, http.StatusNotFound)

		default:
			if strings.HasPrefix(pty.file, "_redir:") {
				status_code = http.StatusMovedPermanently
				w.Header().Set("Location", append_raw_query(pty.file[7:], req))
				w.WriteHeader(status_code)
			} else {
				status_code = WriteEmptyRespHeader(w, http.StatusNotFound)
			}
	}

//done:
	return status_code, nil

oops:
	return status_code, err
}
