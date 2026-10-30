package hodu

import "context"
import "fmt"
import "net/http"
import "strings"
import "text/template"
import "time"

import "golang.org/x/net/websocket"

type server_pty_ws struct {
	S *Server
	Id string
	Auth *HttpAuthConfig
	ws *websocket.Conn
}

type server_rpty_ws struct {
	S *Server
	Id string
	Auth *HttpAuthConfig
	ws *websocket.Conn
}

type server_pty_xterm_file struct {
	ServerCtl
	file string
	mode string
	auth *HttpAuthConfig
}

// ------------------------------------------------------

func (pty *server_pty_ws) Identity() string {
	return pty.Id
}

func (pty *server_pty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var auth *HttpAuthConfig

	auth = pty.Auth
	if auth == nil { auth = pty.S.Cfg.CtlAuth }
	if auth != nil {
		var status int
		status, _ = auth.Authenticate(ws.Request(), "access-token")
		if status != http.StatusOK { ws.Close(); return status, fmt.Errorf("failed to authenticate") }
	}
	return serve_xterm_sessions(ws, func(ctx context.Context, data []string) (*xterm_backend, error) {
		return open_xterm_pty(ctx, pty.S.pty_shell, pty.S.pty_user, func(delta int64) { pty.S.stats.pty_sessions.Add(delta) })
	})
}

func (rpty *server_rpty_ws) Identity() string {
	return rpty.Id
}

func (rpty *server_rpty_ws) ServeWebsocket(ws *websocket.Conn) (int, error) {
	var s *Server
	var req *http.Request
	var token string
	var cts *ServerConn
	var auth *HttpAuthConfig
	//var username string
	//var password string
	var err error

	s = rpty.S
	req = ws.Request()

	// handle authentication using the first message.
	// end this task if authentication fails.
	auth = rpty.Auth
	if auth == nil { auth = s.Cfg.CtlAuth }
	if auth != nil {
		var status_code int
		status_code, _ = auth.Authenticate(req, "access-token")
		if status_code != http.StatusOK {
			ws.Close()
			return status_code, fmt.Errorf("failed to authenticate")
		}
	}

	token = req.FormValue("client-token")
	if token == "" {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("no client token specified")
	}

	if strings.EqualFold(s.Cfg.RptyClientTokenProtection, "rsa-aes-256-gcm") {
		var rsa_aes *RSAAES
		var rsa_token *RSAAESToken

		if s.Cfg.RptyClientTokenRsaKey ==  nil {
			s.log.Write(rpty.Id, LOG_WARN, "[%s] Failed to decrypt protected client token [%s] - no rpty client token rsa key for %s", req.RemoteAddr, token, s.Cfg.RptyClientTokenProtection)
			return http.StatusInternalServerError, fmt.Errorf("client token decryption failure - %s", token)
		}

		rsa_aes = NewRSAAES(s.Cfg.RptyClientTokenRsaKey)
		rsa_token, err = rsa_aes.DecipherToken(token, time.Now())
		if err != nil {
			s.log.Write(rpty.Id, LOG_WARN, "[%s] Failed to decrypt protected client token [%s] - %s", req.RemoteAddr, token, err.Error())
			return http.StatusBadRequest, fmt.Errorf("invalid client token - %s", token)
		}
		token = rsa_token.Token
	}

	cts = s.FindServerConnByClientToken(token)
	if cts == nil {
		ws.Close()
		return http.StatusBadRequest, fmt.Errorf("client token not found - %s", token)
	}

	return serve_xterm_sessions(ws, func(ctx context.Context, data []string) (*xterm_backend, error) {
		return cts.open_xterm_rpty(ctx, ws)
	})
}

// ------------------------------------------------------
//Originally, i used a plain xterm.html and use server_pty_xterm_file to serve
//other files that xterm.html loads. Now, i bundle everything into xterm.html
//so no other files need to be served

func (pty *server_pty_xterm_file) Authenticate(req *http.Request) (int, string) {
	// The parent method ServerCtl.Authenticate() applies
	// authentication to all resources. i want to exclude some files.
	var auth *HttpAuthConfig

	auth = pty.auth
	if auth == nil { auth = pty.S.Cfg.CtlAuth }
	if auth != nil && pty.file == "xterm.html" {
		// this is not a real api call. but at least for xterm.html,
		// i don't bypass authentication and and in addition,
		// i check the value of the "access-token" parameter for
		// jwt authentication if it exists
		return auth.Authenticate(req, "access-token")
	}

	// you can download other files without authentication
	// even if authentication is enabled. but this part must not be reached
	// because xterm.html is the only meanful resource as of now
	return http.StatusOK, ""
}

func (pty *server_pty_xterm_file) ServeHTTP(w http.ResponseWriter, req *http.Request) (int, error) {
	var s *Server
	var status_code int
	var err error

	s = pty.S

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
			if s.xterm_html !=  "" {
				_, err = tmpl.Parse(s.xterm_html)
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
						Mode: pty.mode,
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
