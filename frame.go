package main

import "time"

const NET_TYPE_TCP string = "tcp"

const FRAME_CODE_CLIENT_HELLO uint8 = 1
const FRAME_CODE_SERVER_HELLO uint8 = 2
const FRAME_CODE_SERVER_PEER_OPEN uint8 = 3
const FRAME_CODE_SERVER_PEER_DATA uint8 = 4
const FRAME_CODE_SERVER_PEER_CLOSE uint8 = 5
const FRAME_CODE_SERVER_PEER_ERROR uint8 = 6
const FRAME_CODE_CLIENT_PEER_OPENED uint8 = 7
const FRAME_CODE_CLIENT_PEER_DATA uint8 = 8
const FRAME_CODE_CLIENT_PEER_CLOSED uint8 = 9
const FRAME_CODE_CLIENT_PEER_ERROR uint8 = 10

const FRAME_OPTION_PORT uint8 = 1
const FRAME_OPTION_REQNET4 uint8 = 2
const FRAME_OPTION_REQNET6 uint8 = 3

type FrameHeader struct {
	Len    uint16 // length of whole packet including the header
	Code   uint8
	ChanId uint8
	ConnId uint16
}

type FrameOptionHeader struct {
	Len  uint8
	Code uint8
}

type FreameOptionRetnet4Data struct {
	addr   [4]byte
	pfxlen uint8
}

type FreameOptionRetnet6Data struct {
	addr   [16]byte
	pfxlen uint8
}

type FrameClientHelloData struct {
	AuthKey [64]byte // TODO: How to send this securely???
	ReqChan uint8
	/* this can be followed by variable options */
}

type FrameServerHelloData struct {
	AuthCode uint8
}

func read_chan_with_tmout(chan_bool chan bool, tmout time.Duration) (bool, bool) {
	var tmr *time.Timer
	var b bool
	var timed bool

	tmr = time.NewTimer(tmout)

	select {
	case <-tmr.C:
		timed = true
		b = false
	case b = <-chan_bool:
		tmr.Stop()
		timed = false
	}
	return timed, b
}
