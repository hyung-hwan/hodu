package hodu

import (
	"bufio"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestStringToRouteOptionAndString(t *testing.T) {
	var got RouteOption
	var want RouteOption
	var got_str string

	got = StringToRouteOption("tcp4 ssh")
	want = RouteOption(ROUTE_OPTION_TCP4 | ROUTE_OPTION_SSH)
	if got != want {
		t.Fatalf("unexpected route option: got %v want %v", got, want)
	}

	got_str = got.String()
	if got_str != "tcp4 ssh" {
		t.Fatalf("unexpected route option string %q", got_str)
	}
}

func TestStringToRouteOptionUnknownWordReturnsUnspec(t *testing.T) {
	var got RouteOption
	var want RouteOption

	got = StringToRouteOption("tcp4 unknown")
	want = RouteOption(ROUTE_OPTION_UNSPEC)
	if got != want {
		t.Fatalf("expected unspecified option, got %v", got)
	}
}

func TestDurationHelpers(t *testing.T) {
	var d time.Duration
	var err error
	var got_str string

	d, err = ParseDurationString("1.5")
	if err != nil {
		t.Fatalf("ParseDurationString failed: %v", err)
	}
	if d != 1500*time.Millisecond {
		t.Fatalf("unexpected duration %v", d)
	}

	d, err = ParseDurationString("250ms")
	if err != nil || d != 250*time.Millisecond {
		t.Fatalf("unexpected duration parsing result %v, err=%v", d, err)
	}

	d, err = ParseDurationString("")
	if err != nil || d != 0 {
		t.Fatalf("empty duration should return 0,nil; got %v,%v", d, err)
	}

	if _, err = ParseDurationString("bad-value"); err == nil {
		t.Fatal("expected parse error for invalid duration")
	}

	got_str = DurationToSecString(1500 * time.Millisecond)
	if got_str != "1.500000000" {
		t.Fatalf("unexpected seconds formatting %q", got_str)
	}
}

func TestTCPAddressClassHelpers(t *testing.T) {
	var got string
	var addr *net.TCPAddr

	got = TcpAddrStrClass("127.0.0.1:80")
	if got != "tcp4" {
		t.Fatalf("unexpected class for ipv4 string: %q", got)
	}
	got = TcpAddrStrClass("[::1]:80")
	if got != "tcp6" {
		t.Fatalf("unexpected class for ipv6 string: %q", got)
	}
	got = TcpAddrStrClass("not-an-addr")
	if got != "tcp" {
		t.Fatalf("unexpected class for invalid address: %q", got)
	}

	addr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 80}
	got = TcpAddrClass(addr)
	if got != "tcp4" {
		t.Fatalf("unexpected class for ipv4 TCPAddr: %q", got)
	}
	addr = &net.TCPAddr{IP: net.ParseIP("::1"), Port: 80}
	got = TcpAddrClass(addr)
	if got != "tcp6" {
		t.Fatalf("unexpected class for ipv6 TCPAddr: %q", got)
	}
}

func TestGetRegexSubmatch(t *testing.T) {
	var re *regexp.Regexp
	var got string

	re = regexp.MustCompile(`^(ab)(cd)?$`)
	got = get_regex_submatch(re, "abcd", 1)
	if got != "ab" {
		t.Fatalf("unexpected first submatch %q", got)
	}
	got = get_regex_submatch(re, "ab", 2)
	if got != "" {
		t.Fatalf("optional unmatched group should be empty, got %q", got)
	}
	got = get_regex_submatch(re, "zz", 1)
	if got != "" {
		t.Fatalf("non-matching input should return empty string, got %q", got)
	}
	got = get_regex_submatch(re, "abcd", 5)
	if got != "" {
		t.Fatalf("out-of-range group should return empty string, got %q", got)
	}
}

func TestReadLineLimited(t *testing.T) {
	var r *bufio.Reader
	var line string
	var err error

	r = bufio.NewReader(strings.NewReader("hello\nworld"))
	line, err = read_line_limited(r, 16)
	if err != nil {
		t.Fatalf("unexpected error on first line: %v", err)
	}
	if line != "hello\n" {
		t.Fatalf("unexpected first line %q", line)
	}

	line, err = read_line_limited(r, 16)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF on final line, got %v", err)
	}
	if line != "world" {
		t.Fatalf("unexpected final line %q", line)
	}
}

func TestReadLineLimitedRejectsLongLine(t *testing.T) {
	var r *bufio.Reader
	var err error

	r = bufio.NewReaderSize(strings.NewReader("1234567890\n"), 4)
	_, err = read_line_limited(r, 5)
	if err == nil || !strings.Contains(err.Error(), "line too long") {
		t.Fatalf("expected line too long error, got %v", err)
	}
}

func TestHttpAuthConfigAuthenticateWithEncodedHeaders(t *testing.T) {
	var auth *HttpAuthConfig
	var req *http.Request
	var username string
	var password string
	var status int
	var realm string

	auth = &HttpAuthConfig{
		Enabled: true,
		Realm:   "hodu",
		Creds:   HttpAuthCredMap{"alice": "secret"},
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com/private", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	username = base64.StdEncoding.EncodeToString([]byte("alice"))
	password = base64.StdEncoding.EncodeToString([]byte("secret"))
	req.Header.Set("X-Auth-Username", username)
	req.Header.Set("X-Auth-Password", password)

	status, realm = auth.Authenticate(req)
	if status != http.StatusOK || realm != "" {
		t.Fatalf("unexpected auth result status=%d realm=%q", status, realm)
	}
}

func TestHttpAuthConfigAuthenticateRejectsInvalidBase64(t *testing.T) {
	var auth *HttpAuthConfig
	var req *http.Request
	var status int

	auth = &HttpAuthConfig{
		Enabled: true,
		Realm:   "hodu",
		Creds:   HttpAuthCredMap{"alice": "secret"},
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com/private", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Auth-Username", "%%%")

	status, _ = auth.Authenticate(req)
	if status != http.StatusBadRequest {
		t.Fatalf("expected bad request for invalid header encoding, got %d", status)
	}
}

func TestHttpAuthConfigAccessRuleReject(t *testing.T) {
	var auth *HttpAuthConfig
	var req *http.Request
	var status int

	auth = &HttpAuthConfig{
		Enabled: true,
		Realm:   "hodu",
		Creds:   HttpAuthCredMap{"alice": "secret"},
		AccessRules: []HttpAccessRule{
			{Prefix: "/blocked", Action: HTTP_ACCESS_REJECT},
		},
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com/blocked/path", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	status, _ = auth.Authenticate(req)
	if status != http.StatusForbidden {
		t.Fatalf("expected forbidden status, got %d", status)
	}
}
