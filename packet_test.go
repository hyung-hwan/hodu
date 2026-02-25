package hodu

import (
	"bytes"
	"testing"
)

func TestMakeRouteStartPacket(t *testing.T) {
	var option RouteOption
	var pkt *Packet
	var route *RouteDesc

	option = RouteOption(ROUTE_OPTION_TCP4 | ROUTE_OPTION_SSH)
	pkt = MakeRouteStartPacket(7, option, "127.0.0.1:22", "ssh", "0.0.0.0:0", "0.0.0.0/0")

	if pkt.Kind != PACKET_KIND_ROUTE_START {
		t.Fatalf("unexpected packet kind %v", pkt.Kind)
	}

	route = pkt.GetRoute()
	if route == nil {
		t.Fatal("expected route payload")
	}
	if route.RouteId != 7 {
		t.Fatalf("unexpected route id %d", route.RouteId)
	}
	if route.ServiceOption != uint32(option) {
		t.Fatalf("unexpected route option %d", route.ServiceOption)
	}
	if route.TargetAddrStr != "127.0.0.1:22" || route.TargetName != "ssh" {
		t.Fatalf("unexpected target route payload: %+v", route)
	}
}

func TestMakePeerDataPacket(t *testing.T) {
	var payload []byte
	var pkt *Packet
	var data *PeerData

	payload = []byte("hello-peer")
	pkt = MakePeerDataPacket(3, 9, payload)

	if pkt.Kind != PACKET_KIND_PEER_DATA {
		t.Fatalf("unexpected packet kind %v", pkt.Kind)
	}

	data = pkt.GetData()
	if data == nil {
		t.Fatal("expected peer data payload")
	}
	if data.RouteId != 3 || data.PeerId != 9 {
		t.Fatalf("unexpected ids: route=%d peer=%d", data.RouteId, data.PeerId)
	}
	if !bytes.Equal(data.Data, payload) {
		t.Fatalf("unexpected payload %q", string(data.Data))
	}
}

func TestMakeConnPackets(t *testing.T) {
	var desc *Packet
	var err_pkt *Packet
	var notice *Packet

	desc = MakeConnDescPacket("client-1")
	if desc.Kind != PACKET_KIND_CONN_DESC || desc.GetConn() == nil || desc.GetConn().Token != "client-1" {
		t.Fatalf("unexpected conn desc packet: %+v", desc)
	}

	err_pkt = MakeConnErrorPacket(42, "boom")
	if err_pkt.Kind != PACKET_KIND_CONN_ERROR || err_pkt.GetConnErr() == nil {
		t.Fatalf("unexpected conn error packet: %+v", err_pkt)
	}
	if err_pkt.GetConnErr().ErrorId != 42 || err_pkt.GetConnErr().Text != "boom" {
		t.Fatalf("unexpected conn error payload: %+v", err_pkt.GetConnErr())
	}

	notice = MakeConnNoticePacket("hello")
	if notice.Kind != PACKET_KIND_CONN_NOTICE || notice.GetConnNoti() == nil || notice.GetConnNoti().Text != "hello" {
		t.Fatalf("unexpected conn notice packet: %+v", notice)
	}
}

func TestMakeRptyAndRpxPackets(t *testing.T) {
	var rpty *Packet
	var req []byte
	var rpx *Packet
	var eof *Packet

	rpty = MakeRptyStopPacket(88, "session ended")
	if rpty.Kind != PACKET_KIND_RPTY_STOP || rpty.GetRptyEvt() == nil {
		t.Fatalf("unexpected rpty packet: %+v", rpty)
	}
	if rpty.GetRptyEvt().Id != 88 || string(rpty.GetRptyEvt().Data) != "session ended" {
		t.Fatalf("unexpected rpty payload: %+v", rpty.GetRptyEvt())
	}

	req = []byte("GET / HTTP/1.1\r\n\r\n")
	rpx = MakeRpxStartPacket(12, req)
	if rpx.Kind != PACKET_KIND_RPX_START || rpx.GetRpxEvt() == nil {
		t.Fatalf("unexpected rpx packet: %+v", rpx)
	}
	if rpx.GetRpxEvt().Id != 12 || !bytes.Equal(rpx.GetRpxEvt().Data, req) {
		t.Fatalf("unexpected rpx payload: %+v", rpx.GetRpxEvt())
	}

	eof = MakeRpxEofPacket(12)
	if eof.Kind != PACKET_KIND_RPX_EOF || eof.GetRpxEvt() == nil || eof.GetRpxEvt().Id != 12 {
		t.Fatalf("unexpected rpx eof packet: %+v", eof)
	}
	if len(eof.GetRpxEvt().Data) != 0 {
		t.Fatalf("expected empty eof payload, got %q", string(eof.GetRpxEvt().Data))
	}
}
