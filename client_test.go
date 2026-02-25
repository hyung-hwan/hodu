package hodu_test

import "context"
import "hodu"
import "testing"

type test_logger struct{}

func (l *test_logger) Write(id string, level hodu.LogLevel, fmtstr string, args ...interface{}) {}
func (l *test_logger) WriteWithCallDepth(id string, level hodu.LogLevel, call_depth int, fmtstr string, args ...interface{}) {
}
func (l *test_logger) Rotate() {}
func (l *test_logger) Close()  {}

func TestClient001(t *testing.T) {
	var c *hodu.Client
	var r *hodu.ClientRoute
	var err error

	c = hodu.NewClient(context.Background(), "test-client", &test_logger{}, &hodu.ClientConfig{})

	r, err = c.FindClientRouteByServerPeerSvcPortIdStr("100", "200")
	if err == nil {
		t.Errorf("Search on empty client structure must have failed")
	}
	if r != nil {
		t.Errorf("Main route must not be nil upon no error")
	}
}
