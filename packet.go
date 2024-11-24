package hodu


func MakeRouteStartPacket(route_id uint32, proto ROUTE_PROTO, addr string) *Packet {
	return &Packet{
		Kind: PACKET_KIND_ROUTE_START,
		U: &Packet_Route{Route: &RouteDesc{RouteId: route_id, Proto: proto, AddrStr: addr}}}
}

func MakeRouteStopPacket(route_id uint32, proto ROUTE_PROTO, addr string) *Packet {
	return &Packet{
		Kind: PACKET_KIND_ROUTE_STOP,
		U: &Packet_Route{Route: &RouteDesc{RouteId: route_id, Proto: proto, AddrStr: addr}}}
}

func MakeRouteStartedPacket(route_id uint32, proto ROUTE_PROTO, addr string) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_ROUTE_STARTED,
		U: &Packet_Route{Route: &RouteDesc{RouteId: route_id, Proto: proto, AddrStr: addr}}}
}

func MakeRouteStoppedPacket(route_id uint32, proto ROUTE_PROTO) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_ROUTE_STOPPED,
		U: &Packet_Route{Route: &RouteDesc{RouteId: route_id, Proto: proto}}}
}


func MakePeerStartedPacket(route_id uint32, peer_id uint32) *Packet {
	// the connection from a peer to the server has been established
	return &Packet{Kind: PACKET_KIND_PEER_STARTED,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: route_id, PeerId: peer_id}},
	}
}

func MakePeerStoppedPacket(route_id uint32, peer_id uint32) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_STOPPED,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: route_id, PeerId: peer_id},
	}}
}

func MakePeerAbortedPacket(route_id uint32, peer_id uint32) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_ABORTED,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: route_id, PeerId: peer_id},
	}}
}

func MakePeerEofPacket(route_id uint32, peer_id uint32) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_EOF,
		U: &Packet_Peer{Peer: &PeerDesc{RouteId: route_id, PeerId: peer_id},
	}}
}

func MakePeerDataPacket(route_id uint32, peer_id uint32, data []byte) *Packet {
	return &Packet{Kind: PACKET_KIND_PEER_DATA,
		U: &Packet_Data{Data: &PeerData{RouteId: route_id, PeerId: peer_id, Data: data},
	}}
}
