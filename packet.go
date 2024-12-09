package hodu

type ConnId uint64
type RouteId uint32
type PeerId uint32

func MakeRouteStartPacket(route_id RouteId, proto ROUTE_PROTO, addr string, svcnet string) *Packet {
	return &Packet{
		Kind: PACKET_KIND_ROUTE_START,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceProto: proto, TargetAddrStr: addr, ServiceNetStr: svcnet}}}
}

func MakeRouteStopPacket(route_id RouteId, proto ROUTE_PROTO, addr string, svcnet string) *Packet {
	return &Packet{
		Kind: PACKET_KIND_ROUTE_STOP,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceProto: proto, TargetAddrStr: addr, ServiceNetStr: svcnet}}}
}

func MakeRouteStartedPacket(route_id RouteId, proto ROUTE_PROTO, addr string, svcnet string) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_ROUTE_STARTED,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceProto: proto, TargetAddrStr: addr, ServiceNetStr: svcnet}}}
}

func MakeRouteStoppedPacket(route_id RouteId, proto ROUTE_PROTO, addr string, svcnet string) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_ROUTE_STOPPED,
		U: &Packet_Route{Route: &RouteDesc{RouteId: uint32(route_id), ServiceProto: proto, TargetAddrStr: addr, ServiceNetStr: svcnet}}}
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
