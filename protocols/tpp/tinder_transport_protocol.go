package tpp

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"time"
)

// Flags
const (
	FLAG_HI   = 1 << 0 // 0000 0001
	FLAG_ACK  = 1 << 1 // 0000 0010
	FLAG_NACK = 1 << 2 // 0000 0100
	FLAG_RSD  = 1 << 3 // 0000 1000
	FLAG_RST  = 1 << 4 // 0001 0000
	FLAG_DATA = 1 << 5 // 0010 0000
	FLAG_FRG  = 1 << 6 // 0100 0000
	FLAG_END  = 1 << 7 // 1000 0000

	IDENTIFIER_NUMBER = 0x67 // identifier for TPP packets

	FRAGMENT_METADATA_SIZE = 8 // overhead for fragments (StartSeq + EndSeq)
)

var (
	ErrPacketTooShort = fmt.Errorf("packet too short to be valid TPP packet")
)

type tppHeader struct { // 20 bytes
	Identifier uint8
	Flags      uint8
	Reserved   uint16
	SeqNum     uint32
	AckNum     uint32
	Length     uint32
	Checksum   uint32
}

type tppPacket struct {
	Header   tppHeader
	Data     []byte
	LastSent time.Time
}

// marshal converts the TPP packet into bytes.
func (p *tppPacket) marshal() ([]byte, error) {
	buf := make([]byte, 20+len(p.Data))

	buf[0] = p.Header.Identifier
	buf[1] = p.Header.Flags
	binary.BigEndian.PutUint16(buf[2:4], p.Header.Reserved)
	binary.BigEndian.PutUint32(buf[4:8], p.Header.SeqNum)
	binary.BigEndian.PutUint32(buf[8:12], p.Header.AckNum)
	binary.BigEndian.PutUint32(buf[12:16], p.Header.Length)
	binary.BigEndian.PutUint32(buf[16:20], p.Header.Checksum)

	copy(buf[20:], p.Data)

	return buf, nil
}

// unmarshal converts bytes into a TPP packet.
func unmarshal(data []byte) (*tppPacket, error) {
	if len(data) < 20 {
		return nil, ErrPacketTooShort
	}

	header := tppHeader{
		Identifier: data[0],
		Flags:      data[1],
		Reserved:   binary.BigEndian.Uint16(data[2:4]),
		SeqNum:     binary.BigEndian.Uint32(data[4:8]),
		AckNum:     binary.BigEndian.Uint32(data[8:12]),
		Length:     binary.BigEndian.Uint32(data[12:16]),
		Checksum:   binary.BigEndian.Uint32(data[16:20]),
	}

	packet := &tppPacket{
		Header: header,
		Data:   data[20:],
	}

	return packet, nil
}

// wraps fragment metadata to data
// result: startSeq -> endSeq -> data
func wrapFragmentData(startSeq, endSeq uint32, data []byte) []byte {
	out := make([]byte, 8+len(data))
	binary.BigEndian.PutUint32(out[0:4], startSeq)
	binary.BigEndian.PutUint32(out[4:8], endSeq)
	copy(out[8:], data)
	return out
}

// unwraps the fragment metadata from the payload
func unwrapFragmentData(data []byte) (startSeq, endSeq uint32, content []byte) {
	if len(data) < 8 {
		return 0, 0, data
	}

	startSeq = binary.BigEndian.Uint32(data[0:4])
	endSeq = binary.BigEndian.Uint32(data[4:8])
	content = data[8:]
	return
}

// extracts flags from the TPP packet
func (p *tppPacket) getFlags() []uint {
	var flags []uint
	if p.Header.Flags&FLAG_HI != 0 {
		flags = append(flags, FLAG_HI)
	}
	if p.Header.Flags&FLAG_ACK != 0 {
		flags = append(flags, FLAG_ACK)
	}
	if p.Header.Flags&FLAG_NACK != 0 {
		flags = append(flags, FLAG_NACK)
	}
	if p.Header.Flags&FLAG_RSD != 0 {
		flags = append(flags, FLAG_RSD)
	}
	if p.Header.Flags&FLAG_RST != 0 {
		flags = append(flags, FLAG_RST)
	}
	if p.Header.Flags&FLAG_DATA != 0 {
		flags = append(flags, FLAG_DATA)
	}
	if p.Header.Flags&FLAG_FRG != 0 {
		flags = append(flags, FLAG_FRG)
	}
	if p.Header.Flags&FLAG_END != 0 {
		flags = append(flags, FLAG_END)
	}
	return flags
}

// calculateChecksum computes the checksum for the TPP packet.
func (p *tppPacket) calculateChecksum() uint32 {
	buf := make([]byte, 20+len(p.Data))
	buf[0] = p.Header.Identifier
	buf[1] = p.Header.Flags
	binary.BigEndian.PutUint16(buf[2:4], p.Header.Reserved)
	binary.BigEndian.PutUint32(buf[4:8], p.Header.SeqNum)
	binary.BigEndian.PutUint32(buf[8:12], p.Header.AckNum)
	binary.BigEndian.PutUint32(buf[12:16], p.Header.Length)
	binary.BigEndian.PutUint32(buf[16:20], 0) // ignore checksum
	copy(buf[20:], p.Data)
	return crc32.ChecksumIEEE(buf)
}

// validChecksum checks if the packets checksum is valid.
func (p *tppPacket) validChecksum() bool {
	return p.Header.Checksum == p.calculateChecksum()
}

// isIdentifierValid checks if the packets identifier is valid.
func (p *tppPacket) isIdentifierValid() bool {
	return p.Header.Identifier == IDENTIFIER_NUMBER
}

// createFlagsByte creates a byte representing the flags from a slice of flags.
// used in CLI
func createFlagsByte(flags []uint) uint8 {
	var flagsByte uint8 = 0
	for _, flag := range flags {
		flagsByte |= uint8(flag)
	}
	return flagsByte
}
