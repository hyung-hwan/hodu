package hodu_test

import "context"
import "hodu"
import "testing"

type TestLogger struct {}

func (l *TestLogger) Write(id string, level hodu.LogLevel, fmtstr string, args ...interface{}) {}
func (l *TestLogger) WriteWithCallDepth(id string, level hodu.LogLevel, call_depth int, fmtstr string, args ...interface{}) {}
func (l *TestLogger) Rotate() {}
func (l *TestLogger) Close() {}

func TestClient001(t *testing.T) {
	var c *hodu.Client
	var r *hodu.ClientRoute
	var err error

	c = hodu.NewClient(context.Background(), "test-client", &TestLogger{}, &hodu.ClientConfig{})

	r, err = c.FindClientRouteByServerPeerSvcPortIdStr("100", "200")
	if err == nil { t.Errorf("Search on empty client structure must have failed") }
	if r != nil { t.Errorf("Main route must not be nil upon no error") }
}
