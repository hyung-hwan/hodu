// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.5
// 	protoc        v3.19.6
// source: hodu.proto

package hodu

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ROUTE_OPTION int32

const (
	ROUTE_OPTION_UNSPEC ROUTE_OPTION = 0
	ROUTE_OPTION_TCP    ROUTE_OPTION = 1
	ROUTE_OPTION_TCP4   ROUTE_OPTION = 2
	ROUTE_OPTION_TCP6   ROUTE_OPTION = 4
	ROUTE_OPTION_TTY    ROUTE_OPTION = 8
	ROUTE_OPTION_HTTP   ROUTE_OPTION = 16
	ROUTE_OPTION_HTTPS  ROUTE_OPTION = 32
	ROUTE_OPTION_SSH    ROUTE_OPTION = 64
)

// Enum value maps for ROUTE_OPTION.
var (
	ROUTE_OPTION_name = map[int32]string{
		0:  "UNSPEC",
		1:  "TCP",
		2:  "TCP4",
		4:  "TCP6",
		8:  "TTY",
		16: "HTTP",
		32: "HTTPS",
		64: "SSH",
	}
	ROUTE_OPTION_value = map[string]int32{
		"UNSPEC": 0,
		"TCP":    1,
		"TCP4":   2,
		"TCP6":   4,
		"TTY":    8,
		"HTTP":   16,
		"HTTPS":  32,
		"SSH":    64,
	}
)

func (x ROUTE_OPTION) Enum() *ROUTE_OPTION {
	p := new(ROUTE_OPTION)
	*p = x
	return p
}

func (x ROUTE_OPTION) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ROUTE_OPTION) Descriptor() protoreflect.EnumDescriptor {
	return file_hodu_proto_enumTypes[0].Descriptor()
}

func (ROUTE_OPTION) Type() protoreflect.EnumType {
	return &file_hodu_proto_enumTypes[0]
}

func (x ROUTE_OPTION) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ROUTE_OPTION.Descriptor instead.
func (ROUTE_OPTION) EnumDescriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{0}
}

type PACKET_KIND int32

const (
	PACKET_KIND_RESERVED      PACKET_KIND = 0 // not used
	PACKET_KIND_ROUTE_START   PACKET_KIND = 1
	PACKET_KIND_ROUTE_STOP    PACKET_KIND = 2
	PACKET_KIND_ROUTE_STARTED PACKET_KIND = 3
	PACKET_KIND_ROUTE_STOPPED PACKET_KIND = 4
	PACKET_KIND_PEER_STARTED  PACKET_KIND = 5
	PACKET_KIND_PEER_STOPPED  PACKET_KIND = 6
	PACKET_KIND_PEER_ABORTED  PACKET_KIND = 7
	PACKET_KIND_PEER_EOF      PACKET_KIND = 8
	PACKET_KIND_PEER_DATA     PACKET_KIND = 9
	PACKET_KIND_CONN_DESC     PACKET_KIND = 11
	PACKET_KIND_CONN_ERROR    PACKET_KIND = 12
	PACKET_KIND_CONN_NOTICE   PACKET_KIND = 13
)

// Enum value maps for PACKET_KIND.
var (
	PACKET_KIND_name = map[int32]string{
		0:  "RESERVED",
		1:  "ROUTE_START",
		2:  "ROUTE_STOP",
		3:  "ROUTE_STARTED",
		4:  "ROUTE_STOPPED",
		5:  "PEER_STARTED",
		6:  "PEER_STOPPED",
		7:  "PEER_ABORTED",
		8:  "PEER_EOF",
		9:  "PEER_DATA",
		11: "CONN_DESC",
		12: "CONN_ERROR",
		13: "CONN_NOTICE",
	}
	PACKET_KIND_value = map[string]int32{
		"RESERVED":      0,
		"ROUTE_START":   1,
		"ROUTE_STOP":    2,
		"ROUTE_STARTED": 3,
		"ROUTE_STOPPED": 4,
		"PEER_STARTED":  5,
		"PEER_STOPPED":  6,
		"PEER_ABORTED":  7,
		"PEER_EOF":      8,
		"PEER_DATA":     9,
		"CONN_DESC":     11,
		"CONN_ERROR":    12,
		"CONN_NOTICE":   13,
	}
)

func (x PACKET_KIND) Enum() *PACKET_KIND {
	p := new(PACKET_KIND)
	*p = x
	return p
}

func (x PACKET_KIND) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (PACKET_KIND) Descriptor() protoreflect.EnumDescriptor {
	return file_hodu_proto_enumTypes[1].Descriptor()
}

func (PACKET_KIND) Type() protoreflect.EnumType {
	return &file_hodu_proto_enumTypes[1]
}

func (x PACKET_KIND) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use PACKET_KIND.Descriptor instead.
func (PACKET_KIND) EnumDescriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{1}
}

type Seed struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Version       uint32                 `protobuf:"varint,1,opt,name=Version,proto3" json:"Version,omitempty"`
	Flags         uint64                 `protobuf:"varint,2,opt,name=Flags,proto3" json:"Flags,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Seed) Reset() {
	*x = Seed{}
	mi := &file_hodu_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Seed) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Seed) ProtoMessage() {}

func (x *Seed) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Seed.ProtoReflect.Descriptor instead.
func (*Seed) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{0}
}

func (x *Seed) GetVersion() uint32 {
	if x != nil {
		return x.Version
	}
	return 0
}

func (x *Seed) GetFlags() uint64 {
	if x != nil {
		return x.Flags
	}
	return 0
}

type RouteDesc struct {
	state   protoimpl.MessageState `protogen:"open.v1"`
	RouteId uint32                 `protobuf:"varint,1,opt,name=RouteId,proto3" json:"RouteId,omitempty"`
	// C->S(ROUTE_START): client-side peer address
	// S->C(ROUTE_STARTED): server-side listening address
	TargetAddrStr string `protobuf:"bytes,2,opt,name=TargetAddrStr,proto3" json:"TargetAddrStr,omitempty"`
	// C->S(ROUTE_START): human-readable name of client-side peer
	// S->C(ROUTE_STARTED): clone as sent by C
	TargetName string `protobuf:"bytes,3,opt,name=TargetName,proto3" json:"TargetName,omitempty"`
	// C->S(ROUTE_START): desired listening option on the server-side(e.g. tcp, tcp4, tcp6) +
	//
	//	hint to the service-side peer(e.g. local) +
	//	hint to the client-side peer(e.g. tty, http, https)
	//
	// S->C(ROUTE_STARTED): cloned as sent by C.
	ServiceOption uint32 `protobuf:"varint,4,opt,name=ServiceOption,proto3" json:"ServiceOption,omitempty"`
	// C->S(ROUTE_START): desired lisening address on the service-side
	// S->C(ROUTE_STARTED): cloned as sent by C
	ServiceAddrStr string `protobuf:"bytes,5,opt,name=ServiceAddrStr,proto3" json:"ServiceAddrStr,omitempty"`
	// C->S(ROUTE_START): permitted network of server-side peers.
	// S->C(ROUTE_STARTED): cloned as sent by C.
	ServiceNetStr string `protobuf:"bytes,6,opt,name=ServiceNetStr,proto3" json:"ServiceNetStr,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RouteDesc) Reset() {
	*x = RouteDesc{}
	mi := &file_hodu_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RouteDesc) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RouteDesc) ProtoMessage() {}

func (x *RouteDesc) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RouteDesc.ProtoReflect.Descriptor instead.
func (*RouteDesc) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{1}
}

func (x *RouteDesc) GetRouteId() uint32 {
	if x != nil {
		return x.RouteId
	}
	return 0
}

func (x *RouteDesc) GetTargetAddrStr() string {
	if x != nil {
		return x.TargetAddrStr
	}
	return ""
}

func (x *RouteDesc) GetTargetName() string {
	if x != nil {
		return x.TargetName
	}
	return ""
}

func (x *RouteDesc) GetServiceOption() uint32 {
	if x != nil {
		return x.ServiceOption
	}
	return 0
}

func (x *RouteDesc) GetServiceAddrStr() string {
	if x != nil {
		return x.ServiceAddrStr
	}
	return ""
}

func (x *RouteDesc) GetServiceNetStr() string {
	if x != nil {
		return x.ServiceNetStr
	}
	return ""
}

type PeerDesc struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	RouteId       uint32                 `protobuf:"varint,1,opt,name=RouteId,proto3" json:"RouteId,omitempty"`
	PeerId        uint32                 `protobuf:"varint,2,opt,name=PeerId,proto3" json:"PeerId,omitempty"`
	RemoteAddrStr string                 `protobuf:"bytes,3,opt,name=RemoteAddrStr,proto3" json:"RemoteAddrStr,omitempty"`
	LocalAddrStr  string                 `protobuf:"bytes,4,opt,name=LocalAddrStr,proto3" json:"LocalAddrStr,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *PeerDesc) Reset() {
	*x = PeerDesc{}
	mi := &file_hodu_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *PeerDesc) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PeerDesc) ProtoMessage() {}

func (x *PeerDesc) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PeerDesc.ProtoReflect.Descriptor instead.
func (*PeerDesc) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{2}
}

func (x *PeerDesc) GetRouteId() uint32 {
	if x != nil {
		return x.RouteId
	}
	return 0
}

func (x *PeerDesc) GetPeerId() uint32 {
	if x != nil {
		return x.PeerId
	}
	return 0
}

func (x *PeerDesc) GetRemoteAddrStr() string {
	if x != nil {
		return x.RemoteAddrStr
	}
	return ""
}

func (x *PeerDesc) GetLocalAddrStr() string {
	if x != nil {
		return x.LocalAddrStr
	}
	return ""
}

type PeerData struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	RouteId       uint32                 `protobuf:"varint,1,opt,name=RouteId,proto3" json:"RouteId,omitempty"`
	PeerId        uint32                 `protobuf:"varint,2,opt,name=PeerId,proto3" json:"PeerId,omitempty"`
	Data          []byte                 `protobuf:"bytes,3,opt,name=Data,proto3" json:"Data,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *PeerData) Reset() {
	*x = PeerData{}
	mi := &file_hodu_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *PeerData) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PeerData) ProtoMessage() {}

func (x *PeerData) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PeerData.ProtoReflect.Descriptor instead.
func (*PeerData) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{3}
}

func (x *PeerData) GetRouteId() uint32 {
	if x != nil {
		return x.RouteId
	}
	return 0
}

func (x *PeerData) GetPeerId() uint32 {
	if x != nil {
		return x.PeerId
	}
	return 0
}

func (x *PeerData) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

type ConnDesc struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Token         string                 `protobuf:"bytes,1,opt,name=Token,proto3" json:"Token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ConnDesc) Reset() {
	*x = ConnDesc{}
	mi := &file_hodu_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ConnDesc) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnDesc) ProtoMessage() {}

func (x *ConnDesc) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnDesc.ProtoReflect.Descriptor instead.
func (*ConnDesc) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{4}
}

func (x *ConnDesc) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

type ConnError struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	ErrorId       uint32                 `protobuf:"varint,1,opt,name=ErrorId,proto3" json:"ErrorId,omitempty"`
	Text          string                 `protobuf:"bytes,2,opt,name=Text,proto3" json:"Text,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ConnError) Reset() {
	*x = ConnError{}
	mi := &file_hodu_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ConnError) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnError) ProtoMessage() {}

func (x *ConnError) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnError.ProtoReflect.Descriptor instead.
func (*ConnError) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{5}
}

func (x *ConnError) GetErrorId() uint32 {
	if x != nil {
		return x.ErrorId
	}
	return 0
}

func (x *ConnError) GetText() string {
	if x != nil {
		return x.Text
	}
	return ""
}

type ConnNotice struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Text          string                 `protobuf:"bytes,1,opt,name=Text,proto3" json:"Text,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ConnNotice) Reset() {
	*x = ConnNotice{}
	mi := &file_hodu_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ConnNotice) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ConnNotice) ProtoMessage() {}

func (x *ConnNotice) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ConnNotice.ProtoReflect.Descriptor instead.
func (*ConnNotice) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{6}
}

func (x *ConnNotice) GetText() string {
	if x != nil {
		return x.Text
	}
	return ""
}

type Packet struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	Kind  PACKET_KIND            `protobuf:"varint,1,opt,name=Kind,proto3,enum=PACKET_KIND" json:"Kind,omitempty"`
	// Types that are valid to be assigned to U:
	//
	//	*Packet_Route
	//	*Packet_Peer
	//	*Packet_Data
	//	*Packet_Conn
	//	*Packet_ConnErr
	//	*Packet_ConnNoti
	U             isPacket_U `protobuf_oneof:"U"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Packet) Reset() {
	*x = Packet{}
	mi := &file_hodu_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Packet) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Packet) ProtoMessage() {}

func (x *Packet) ProtoReflect() protoreflect.Message {
	mi := &file_hodu_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Packet.ProtoReflect.Descriptor instead.
func (*Packet) Descriptor() ([]byte, []int) {
	return file_hodu_proto_rawDescGZIP(), []int{7}
}

func (x *Packet) GetKind() PACKET_KIND {
	if x != nil {
		return x.Kind
	}
	return PACKET_KIND_RESERVED
}

func (x *Packet) GetU() isPacket_U {
	if x != nil {
		return x.U
	}
	return nil
}

func (x *Packet) GetRoute() *RouteDesc {
	if x != nil {
		if x, ok := x.U.(*Packet_Route); ok {
			return x.Route
		}
	}
	return nil
}

func (x *Packet) GetPeer() *PeerDesc {
	if x != nil {
		if x, ok := x.U.(*Packet_Peer); ok {
			return x.Peer
		}
	}
	return nil
}

func (x *Packet) GetData() *PeerData {
	if x != nil {
		if x, ok := x.U.(*Packet_Data); ok {
			return x.Data
		}
	}
	return nil
}

func (x *Packet) GetConn() *ConnDesc {
	if x != nil {
		if x, ok := x.U.(*Packet_Conn); ok {
			return x.Conn
		}
	}
	return nil
}

func (x *Packet) GetConnErr() *ConnError {
	if x != nil {
		if x, ok := x.U.(*Packet_ConnErr); ok {
			return x.ConnErr
		}
	}
	return nil
}

func (x *Packet) GetConnNoti() *ConnNotice {
	if x != nil {
		if x, ok := x.U.(*Packet_ConnNoti); ok {
			return x.ConnNoti
		}
	}
	return nil
}

type isPacket_U interface {
	isPacket_U()
}

type Packet_Route struct {
	Route *RouteDesc `protobuf:"bytes,2,opt,name=Route,proto3,oneof"`
}

type Packet_Peer struct {
	Peer *PeerDesc `protobuf:"bytes,3,opt,name=Peer,proto3,oneof"`
}

type Packet_Data struct {
	Data *PeerData `protobuf:"bytes,4,opt,name=Data,proto3,oneof"`
}

type Packet_Conn struct {
	Conn *ConnDesc `protobuf:"bytes,5,opt,name=Conn,proto3,oneof"`
}

type Packet_ConnErr struct {
	ConnErr *ConnError `protobuf:"bytes,6,opt,name=ConnErr,proto3,oneof"`
}

type Packet_ConnNoti struct {
	ConnNoti *ConnNotice `protobuf:"bytes,7,opt,name=ConnNoti,proto3,oneof"`
}

func (*Packet_Route) isPacket_U() {}

func (*Packet_Peer) isPacket_U() {}

func (*Packet_Data) isPacket_U() {}

func (*Packet_Conn) isPacket_U() {}

func (*Packet_ConnErr) isPacket_U() {}

func (*Packet_ConnNoti) isPacket_U() {}

var File_hodu_proto protoreflect.FileDescriptor

var file_hodu_proto_rawDesc = string([]byte{
	0x0a, 0x0a, 0x68, 0x6f, 0x64, 0x75, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x36, 0x0a, 0x04,
	0x53, 0x65, 0x65, 0x64, 0x12, 0x18, 0x0a, 0x07, 0x56, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x0d, 0x52, 0x07, 0x56, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x12, 0x14,
	0x0a, 0x05, 0x46, 0x6c, 0x61, 0x67, 0x73, 0x18, 0x02, 0x20, 0x01, 0x28, 0x04, 0x52, 0x05, 0x46,
	0x6c, 0x61, 0x67, 0x73, 0x22, 0xdf, 0x01, 0x0a, 0x09, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x44, 0x65,
	0x73, 0x63, 0x12, 0x18, 0x0a, 0x07, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x49, 0x64, 0x18, 0x01, 0x20,
	0x01, 0x28, 0x0d, 0x52, 0x07, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x49, 0x64, 0x12, 0x24, 0x0a, 0x0d,
	0x54, 0x61, 0x72, 0x67, 0x65, 0x74, 0x41, 0x64, 0x64, 0x72, 0x53, 0x74, 0x72, 0x18, 0x02, 0x20,
	0x01, 0x28, 0x09, 0x52, 0x0d, 0x54, 0x61, 0x72, 0x67, 0x65, 0x74, 0x41, 0x64, 0x64, 0x72, 0x53,
	0x74, 0x72, 0x12, 0x1e, 0x0a, 0x0a, 0x54, 0x61, 0x72, 0x67, 0x65, 0x74, 0x4e, 0x61, 0x6d, 0x65,
	0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0a, 0x54, 0x61, 0x72, 0x67, 0x65, 0x74, 0x4e, 0x61,
	0x6d, 0x65, 0x12, 0x24, 0x0a, 0x0d, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x4f, 0x70, 0x74,
	0x69, 0x6f, 0x6e, 0x18, 0x04, 0x20, 0x01, 0x28, 0x0d, 0x52, 0x0d, 0x53, 0x65, 0x72, 0x76, 0x69,
	0x63, 0x65, 0x4f, 0x70, 0x74, 0x69, 0x6f, 0x6e, 0x12, 0x26, 0x0a, 0x0e, 0x53, 0x65, 0x72, 0x76,
	0x69, 0x63, 0x65, 0x41, 0x64, 0x64, 0x72, 0x53, 0x74, 0x72, 0x18, 0x05, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x0e, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x41, 0x64, 0x64, 0x72, 0x53, 0x74, 0x72,
	0x12, 0x24, 0x0a, 0x0d, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65, 0x4e, 0x65, 0x74, 0x53, 0x74,
	0x72, 0x18, 0x06, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0d, 0x53, 0x65, 0x72, 0x76, 0x69, 0x63, 0x65,
	0x4e, 0x65, 0x74, 0x53, 0x74, 0x72, 0x22, 0x86, 0x01, 0x0a, 0x08, 0x50, 0x65, 0x65, 0x72, 0x44,
	0x65, 0x73, 0x63, 0x12, 0x18, 0x0a, 0x07, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x49, 0x64, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x0d, 0x52, 0x07, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x49, 0x64, 0x12, 0x16, 0x0a,
	0x06, 0x50, 0x65, 0x65, 0x72, 0x49, 0x64, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0d, 0x52, 0x06, 0x50,
	0x65, 0x65, 0x72, 0x49, 0x64, 0x12, 0x24, 0x0a, 0x0d, 0x52, 0x65, 0x6d, 0x6f, 0x74, 0x65, 0x41,
	0x64, 0x64, 0x72, 0x53, 0x74, 0x72, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0d, 0x52, 0x65,
	0x6d, 0x6f, 0x74, 0x65, 0x41, 0x64, 0x64, 0x72, 0x53, 0x74, 0x72, 0x12, 0x22, 0x0a, 0x0c, 0x4c,
	0x6f, 0x63, 0x61, 0x6c, 0x41, 0x64, 0x64, 0x72, 0x53, 0x74, 0x72, 0x18, 0x04, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x0c, 0x4c, 0x6f, 0x63, 0x61, 0x6c, 0x41, 0x64, 0x64, 0x72, 0x53, 0x74, 0x72, 0x22,
	0x50, 0x0a, 0x08, 0x50, 0x65, 0x65, 0x72, 0x44, 0x61, 0x74, 0x61, 0x12, 0x18, 0x0a, 0x07, 0x52,
	0x6f, 0x75, 0x74, 0x65, 0x49, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0d, 0x52, 0x07, 0x52, 0x6f,
	0x75, 0x74, 0x65, 0x49, 0x64, 0x12, 0x16, 0x0a, 0x06, 0x50, 0x65, 0x65, 0x72, 0x49, 0x64, 0x18,
	0x02, 0x20, 0x01, 0x28, 0x0d, 0x52, 0x06, 0x50, 0x65, 0x65, 0x72, 0x49, 0x64, 0x12, 0x12, 0x0a,
	0x04, 0x44, 0x61, 0x74, 0x61, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x04, 0x44, 0x61, 0x74,
	0x61, 0x22, 0x20, 0x0a, 0x08, 0x43, 0x6f, 0x6e, 0x6e, 0x44, 0x65, 0x73, 0x63, 0x12, 0x14, 0x0a,
	0x05, 0x54, 0x6f, 0x6b, 0x65, 0x6e, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x54, 0x6f,
	0x6b, 0x65, 0x6e, 0x22, 0x39, 0x0a, 0x09, 0x43, 0x6f, 0x6e, 0x6e, 0x45, 0x72, 0x72, 0x6f, 0x72,
	0x12, 0x18, 0x0a, 0x07, 0x45, 0x72, 0x72, 0x6f, 0x72, 0x49, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28,
	0x0d, 0x52, 0x07, 0x45, 0x72, 0x72, 0x6f, 0x72, 0x49, 0x64, 0x12, 0x12, 0x0a, 0x04, 0x54, 0x65,
	0x78, 0x74, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x54, 0x65, 0x78, 0x74, 0x22, 0x20,
	0x0a, 0x0a, 0x43, 0x6f, 0x6e, 0x6e, 0x4e, 0x6f, 0x74, 0x69, 0x63, 0x65, 0x12, 0x12, 0x0a, 0x04,
	0x54, 0x65, 0x78, 0x74, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x54, 0x65, 0x78, 0x74,
	0x22, 0x89, 0x02, 0x0a, 0x06, 0x50, 0x61, 0x63, 0x6b, 0x65, 0x74, 0x12, 0x20, 0x0a, 0x04, 0x4b,
	0x69, 0x6e, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x0c, 0x2e, 0x50, 0x41, 0x43, 0x4b,
	0x45, 0x54, 0x5f, 0x4b, 0x49, 0x4e, 0x44, 0x52, 0x04, 0x4b, 0x69, 0x6e, 0x64, 0x12, 0x22, 0x0a,
	0x05, 0x52, 0x6f, 0x75, 0x74, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0a, 0x2e, 0x52,
	0x6f, 0x75, 0x74, 0x65, 0x44, 0x65, 0x73, 0x63, 0x48, 0x00, 0x52, 0x05, 0x52, 0x6f, 0x75, 0x74,
	0x65, 0x12, 0x1f, 0x0a, 0x04, 0x50, 0x65, 0x65, 0x72, 0x18, 0x03, 0x20, 0x01, 0x28, 0x0b, 0x32,
	0x09, 0x2e, 0x50, 0x65, 0x65, 0x72, 0x44, 0x65, 0x73, 0x63, 0x48, 0x00, 0x52, 0x04, 0x50, 0x65,
	0x65, 0x72, 0x12, 0x1f, 0x0a, 0x04, 0x44, 0x61, 0x74, 0x61, 0x18, 0x04, 0x20, 0x01, 0x28, 0x0b,
	0x32, 0x09, 0x2e, 0x50, 0x65, 0x65, 0x72, 0x44, 0x61, 0x74, 0x61, 0x48, 0x00, 0x52, 0x04, 0x44,
	0x61, 0x74, 0x61, 0x12, 0x1f, 0x0a, 0x04, 0x43, 0x6f, 0x6e, 0x6e, 0x18, 0x05, 0x20, 0x01, 0x28,
	0x0b, 0x32, 0x09, 0x2e, 0x43, 0x6f, 0x6e, 0x6e, 0x44, 0x65, 0x73, 0x63, 0x48, 0x00, 0x52, 0x04,
	0x43, 0x6f, 0x6e, 0x6e, 0x12, 0x26, 0x0a, 0x07, 0x43, 0x6f, 0x6e, 0x6e, 0x45, 0x72, 0x72, 0x18,
	0x06, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0a, 0x2e, 0x43, 0x6f, 0x6e, 0x6e, 0x45, 0x72, 0x72, 0x6f,
	0x72, 0x48, 0x00, 0x52, 0x07, 0x43, 0x6f, 0x6e, 0x6e, 0x45, 0x72, 0x72, 0x12, 0x29, 0x0a, 0x08,
	0x43, 0x6f, 0x6e, 0x6e, 0x4e, 0x6f, 0x74, 0x69, 0x18, 0x07, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x0b,
	0x2e, 0x43, 0x6f, 0x6e, 0x6e, 0x4e, 0x6f, 0x74, 0x69, 0x63, 0x65, 0x48, 0x00, 0x52, 0x08, 0x43,
	0x6f, 0x6e, 0x6e, 0x4e, 0x6f, 0x74, 0x69, 0x42, 0x03, 0x0a, 0x01, 0x55, 0x2a, 0x5e, 0x0a, 0x0c,
	0x52, 0x4f, 0x55, 0x54, 0x45, 0x5f, 0x4f, 0x50, 0x54, 0x49, 0x4f, 0x4e, 0x12, 0x0a, 0x0a, 0x06,
	0x55, 0x4e, 0x53, 0x50, 0x45, 0x43, 0x10, 0x00, 0x12, 0x07, 0x0a, 0x03, 0x54, 0x43, 0x50, 0x10,
	0x01, 0x12, 0x08, 0x0a, 0x04, 0x54, 0x43, 0x50, 0x34, 0x10, 0x02, 0x12, 0x08, 0x0a, 0x04, 0x54,
	0x43, 0x50, 0x36, 0x10, 0x04, 0x12, 0x07, 0x0a, 0x03, 0x54, 0x54, 0x59, 0x10, 0x08, 0x12, 0x08,
	0x0a, 0x04, 0x48, 0x54, 0x54, 0x50, 0x10, 0x10, 0x12, 0x09, 0x0a, 0x05, 0x48, 0x54, 0x54, 0x50,
	0x53, 0x10, 0x20, 0x12, 0x07, 0x0a, 0x03, 0x53, 0x53, 0x48, 0x10, 0x40, 0x2a, 0xe5, 0x01, 0x0a,
	0x0b, 0x50, 0x41, 0x43, 0x4b, 0x45, 0x54, 0x5f, 0x4b, 0x49, 0x4e, 0x44, 0x12, 0x0c, 0x0a, 0x08,
	0x52, 0x45, 0x53, 0x45, 0x52, 0x56, 0x45, 0x44, 0x10, 0x00, 0x12, 0x0f, 0x0a, 0x0b, 0x52, 0x4f,
	0x55, 0x54, 0x45, 0x5f, 0x53, 0x54, 0x41, 0x52, 0x54, 0x10, 0x01, 0x12, 0x0e, 0x0a, 0x0a, 0x52,
	0x4f, 0x55, 0x54, 0x45, 0x5f, 0x53, 0x54, 0x4f, 0x50, 0x10, 0x02, 0x12, 0x11, 0x0a, 0x0d, 0x52,
	0x4f, 0x55, 0x54, 0x45, 0x5f, 0x53, 0x54, 0x41, 0x52, 0x54, 0x45, 0x44, 0x10, 0x03, 0x12, 0x11,
	0x0a, 0x0d, 0x52, 0x4f, 0x55, 0x54, 0x45, 0x5f, 0x53, 0x54, 0x4f, 0x50, 0x50, 0x45, 0x44, 0x10,
	0x04, 0x12, 0x10, 0x0a, 0x0c, 0x50, 0x45, 0x45, 0x52, 0x5f, 0x53, 0x54, 0x41, 0x52, 0x54, 0x45,
	0x44, 0x10, 0x05, 0x12, 0x10, 0x0a, 0x0c, 0x50, 0x45, 0x45, 0x52, 0x5f, 0x53, 0x54, 0x4f, 0x50,
	0x50, 0x45, 0x44, 0x10, 0x06, 0x12, 0x10, 0x0a, 0x0c, 0x50, 0x45, 0x45, 0x52, 0x5f, 0x41, 0x42,
	0x4f, 0x52, 0x54, 0x45, 0x44, 0x10, 0x07, 0x12, 0x0c, 0x0a, 0x08, 0x50, 0x45, 0x45, 0x52, 0x5f,
	0x45, 0x4f, 0x46, 0x10, 0x08, 0x12, 0x0d, 0x0a, 0x09, 0x50, 0x45, 0x45, 0x52, 0x5f, 0x44, 0x41,
	0x54, 0x41, 0x10, 0x09, 0x12, 0x0d, 0x0a, 0x09, 0x43, 0x4f, 0x4e, 0x4e, 0x5f, 0x44, 0x45, 0x53,
	0x43, 0x10, 0x0b, 0x12, 0x0e, 0x0a, 0x0a, 0x43, 0x4f, 0x4e, 0x4e, 0x5f, 0x45, 0x52, 0x52, 0x4f,
	0x52, 0x10, 0x0c, 0x12, 0x0f, 0x0a, 0x0b, 0x43, 0x4f, 0x4e, 0x4e, 0x5f, 0x4e, 0x4f, 0x54, 0x49,
	0x43, 0x45, 0x10, 0x0d, 0x32, 0x49, 0x0a, 0x04, 0x48, 0x6f, 0x64, 0x75, 0x12, 0x19, 0x0a, 0x07,
	0x47, 0x65, 0x74, 0x53, 0x65, 0x65, 0x64, 0x12, 0x05, 0x2e, 0x53, 0x65, 0x65, 0x64, 0x1a, 0x05,
	0x2e, 0x53, 0x65, 0x65, 0x64, 0x22, 0x00, 0x12, 0x26, 0x0a, 0x0c, 0x50, 0x61, 0x63, 0x6b, 0x65,
	0x74, 0x53, 0x74, 0x72, 0x65, 0x61, 0x6d, 0x12, 0x07, 0x2e, 0x50, 0x61, 0x63, 0x6b, 0x65, 0x74,
	0x1a, 0x07, 0x2e, 0x50, 0x61, 0x63, 0x6b, 0x65, 0x74, 0x22, 0x00, 0x28, 0x01, 0x30, 0x01, 0x42,
	0x08, 0x5a, 0x06, 0x2e, 0x2f, 0x68, 0x6f, 0x64, 0x75, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f,
	0x33,
})

var (
	file_hodu_proto_rawDescOnce sync.Once
	file_hodu_proto_rawDescData []byte
)

func file_hodu_proto_rawDescGZIP() []byte {
	file_hodu_proto_rawDescOnce.Do(func() {
		file_hodu_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_hodu_proto_rawDesc), len(file_hodu_proto_rawDesc)))
	})
	return file_hodu_proto_rawDescData
}

var file_hodu_proto_enumTypes = make([]protoimpl.EnumInfo, 2)
var file_hodu_proto_msgTypes = make([]protoimpl.MessageInfo, 8)
var file_hodu_proto_goTypes = []any{
	(ROUTE_OPTION)(0),  // 0: ROUTE_OPTION
	(PACKET_KIND)(0),   // 1: PACKET_KIND
	(*Seed)(nil),       // 2: Seed
	(*RouteDesc)(nil),  // 3: RouteDesc
	(*PeerDesc)(nil),   // 4: PeerDesc
	(*PeerData)(nil),   // 5: PeerData
	(*ConnDesc)(nil),   // 6: ConnDesc
	(*ConnError)(nil),  // 7: ConnError
	(*ConnNotice)(nil), // 8: ConnNotice
	(*Packet)(nil),     // 9: Packet
}
var file_hodu_proto_depIdxs = []int32{
	1, // 0: Packet.Kind:type_name -> PACKET_KIND
	3, // 1: Packet.Route:type_name -> RouteDesc
	4, // 2: Packet.Peer:type_name -> PeerDesc
	5, // 3: Packet.Data:type_name -> PeerData
	6, // 4: Packet.Conn:type_name -> ConnDesc
	7, // 5: Packet.ConnErr:type_name -> ConnError
	8, // 6: Packet.ConnNoti:type_name -> ConnNotice
	2, // 7: Hodu.GetSeed:input_type -> Seed
	9, // 8: Hodu.PacketStream:input_type -> Packet
	2, // 9: Hodu.GetSeed:output_type -> Seed
	9, // 10: Hodu.PacketStream:output_type -> Packet
	9, // [9:11] is the sub-list for method output_type
	7, // [7:9] is the sub-list for method input_type
	7, // [7:7] is the sub-list for extension type_name
	7, // [7:7] is the sub-list for extension extendee
	0, // [0:7] is the sub-list for field type_name
}

func init() { file_hodu_proto_init() }
func file_hodu_proto_init() {
	if File_hodu_proto != nil {
		return
	}
	file_hodu_proto_msgTypes[7].OneofWrappers = []any{
		(*Packet_Route)(nil),
		(*Packet_Peer)(nil),
		(*Packet_Data)(nil),
		(*Packet_Conn)(nil),
		(*Packet_ConnErr)(nil),
		(*Packet_ConnNoti)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_hodu_proto_rawDesc), len(file_hodu_proto_rawDesc)),
			NumEnums:      2,
			NumMessages:   8,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_hodu_proto_goTypes,
		DependencyIndexes: file_hodu_proto_depIdxs,
		EnumInfos:         file_hodu_proto_enumTypes,
		MessageInfos:      file_hodu_proto_msgTypes,
	}.Build()
	File_hodu_proto = out.File
	file_hodu_proto_goTypes = nil
	file_hodu_proto_depIdxs = nil
}
