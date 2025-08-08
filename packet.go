package hodu

type ConnId      uint64
type RouteId     uint32 // keep this in sync with the type of RouteId in hodu.proto
type PeerId      uint32 // keep this in sync with the type of RouteId in hodu.proto
type RouteOption uint32

type ConnRouteId struct {
	conn_id ConnId
	route_id RouteId
}

func MakeRouteStartPacket(route_id RouteId, proto RouteOption, ptc_addr string, ptc_name string, svc_addr string, svc_net string) *Packet {
	return &Packet{
		Kind: PACKET_KIND_ROUTE_START,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceOption: uint32(proto), TargetAddrStr: ptc_addr, TargetName: ptc_name, ServiceAddrStr: svc_addr, ServiceNetStr: svc_net}}}
}

func MakeRouteStopPacket(route_id RouteId, proto RouteOption, ptc_addr string, ptc_name string, svc_addr string, svc_net string) *Packet {
	return &Packet{
		Kind: PACKET_KIND_ROUTE_STOP,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceOption: uint32(proto), TargetAddrStr: ptc_addr, TargetName: ptc_name, ServiceAddrStr: svc_addr, ServiceNetStr: svc_net}}}
}

func MakeRouteStartedPacket(route_id RouteId, proto RouteOption, addr string, ptc_name string, svc_addr string, svc_net string) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_ROUTE_STARTED,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceOption: uint32(proto), TargetAddrStr: addr, TargetName: ptc_name, ServiceAddrStr: svc_addr, ServiceNetStr: svc_net}}}
}

func MakeRouteStoppedPacket(route_id RouteId, proto RouteOption, addr string, ptc_name string, svc_addr string, svc_net string) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_ROUTE_STOPPED,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceOption: uint32(proto), TargetAddrStr: addr, TargetName: ptc_name, ServiceAddrStr: svc_addr, ServiceNetStr: svc_net}}}
}

func MakePeerStartedPacket(route_id RouteId, peer_id PeerId, remote_addr string, local_addr string) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_PEER_STARTED,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: uint32(route_id), PeerId: uint32(peer_id), RemoteAddrStr: remote_addr, LocalAddrStr: local_addr}},
	}
}

func MakePeerStoppedPacket(route_id RouteId, peer_id PeerId, remote_addr string, local_addr string) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_STOPPED,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: uint32(route_id), PeerId: uint32(peer_id), RemoteAddrStr: remote_addr, LocalAddrStr: local_addr}},
	}
}

func MakePeerAbortedPacket(route_id RouteId, peer_id PeerId, remote_addr string, local_addr string) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_ABORTED,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: uint32(route_id), PeerId: uint32(peer_id), RemoteAddrStr: remote_addr, LocalAddrStr: local_addr}},
	}
}

func MakePeerEofPacket(route_id RouteId, peer_id PeerId) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_EOF,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: uint32(route_id), PeerId: uint32(peer_id)}}}
}

func MakePeerDataPacket(route_id RouteId, peer_id PeerId, data []byte) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_DATA,
		U: &Packet_Data{Data: &PeerData{RouteId: uint32(route_id), PeerId: uint32(peer_id), Data: data}}}
}

func MakeConnDescPacket(token string) *Packet {
	return &Packet{Kind: PACKET_KIND_CONN_DESC,U: &Packet_Conn{Conn: &ConnDesc{Token: token}}}
}

func MakeConnErrorPacket(error_id uint32, msg string) *Packet {
	return &Packet{Kind: PACKET_KIND_CONN_ERROR, U: &Packet_ConnErr{ConnErr: &ConnError{ErrorId: error_id, Text: msg}}}
}

func MakeConnNoticePacket(msg string) *Packet {
	return &Packet{Kind: PACKET_KIND_CONN_NOTICE, U: &Packet_ConnNoti{ConnNoti: &ConnNotice{Text: msg}}}
}

func MakeRptyStartPacket(token string) *Packet {
	return &Packet{Kind: PACKET_KIND_RPTY_START, U: &Packet_Rpty{Rpty: &RptyEvent{Token: token}}}
}

func MakeRptyStopPacket(token string) *Packet {
	return &Packet{Kind: PACKET_KIND_RPTY_START, U: &Packet_Rpty{Rpty: &RptyEvent{Token: token}}}
}

func MakeRptyDataPacket(token string, data []byte) *Packet {
	return &Packet{Kind: PACKET_KIND_RPTY_START, U: &Packet_Rpty{Rpty: &RptyEvent{Token: token, Data: data}}}
}
