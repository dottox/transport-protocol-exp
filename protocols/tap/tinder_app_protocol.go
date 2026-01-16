package tap

import "fmt"

// FLAGS
const (
	FLAG_HELLO = 1 << 0 // 0000 0001 C -> S : (Start connection (register/login))
	FLAG_FIND  = 1 << 1 // 0000 0010 : (ask and return a possible match)
	FLAG_PROP  = 1 << 2 // Unused 0000 0100 C -> C : (Proposal to match between clients)
	FLAG_COMM  = 1 << 3 // Unused 0000 1000 C -> C : (Accepts the match proposal)
	FLAG_SIGN  = 1 << 4 // Unused 0001 0000 C -> S : (Match's signature to the server, by both clients)
	FLAG_DATA  = 1 << 5 // 0010 0000 C -> C : (Data packet (chat message or image)
	FLAG_ERR   = 1 << 6 // 0100 0000 : (Error in latest packet)
)

var (
	ErrPacketTooShort = fmt.Errorf("packet too short to be valid TAP packet")
)

type TAPHeader struct {
	Flags uint8 // 1 byte
}

type TAPPacket struct {
	Header TAPHeader
	Data   []byte
}

// Marshal converts the TAP packet into bytes.
func (p *TAPPacket) Marshal() ([]byte, error) {
	buf := make([]byte, 1+len(p.Data))

	// adding header
	buf[0] = p.Header.Flags

	// adding data
	copy(buf[1:], p.Data)

	return buf, nil
}

// Unmarshal converts bytes into a TAP packet.
func Unmarshal(data []byte) (*TAPPacket, error) {
	if len(data) < 1 {
		return nil, ErrPacketTooShort
	}

	header := TAPHeader{
		Flags: data[0],
	}

	packet := &TAPPacket{
		Header: header,
		Data:   data[1:],
	}

	return packet, nil
}

// GetFlags returns a slice of flags set in the TAP packet.
func (p *TAPPacket) GetFlags() []uint {
	var flags []uint

	if p.Header.Flags&FLAG_HELLO != 0 {
		flags = append(flags, FLAG_HELLO)
	}
	if p.Header.Flags&FLAG_FIND != 0 {
		flags = append(flags, FLAG_FIND)
	}
	if p.Header.Flags&FLAG_PROP != 0 {
		flags = append(flags, FLAG_PROP)
	}
	if p.Header.Flags&FLAG_COMM != 0 {
		flags = append(flags, FLAG_COMM)
	}
	if p.Header.Flags&FLAG_SIGN != 0 {
		flags = append(flags, FLAG_SIGN)
	}
	if p.Header.Flags&FLAG_DATA != 0 {
		flags = append(flags, FLAG_DATA)
	}
	if p.Header.Flags&FLAG_ERR != 0 {
		flags = append(flags, FLAG_ERR)
	}

	return flags
}

// CreatePacket creates a TAP packet with the given data and flags.
func CreatePacket(data []byte, flags ...uint) *TAPPacket {
	return &TAPPacket{
		Header: TAPHeader{
			Flags: createFlagsByte(flags),
		},
		Data: data,
	}
}

// createFlagsByte creates a byte representing the combination of the given flags.
func createFlagsByte(flags []uint) uint8 {
	var flagsByte uint8 = 0
	for _, flag := range flags {
		flagsByte |= uint8(flag)
	}
	return flagsByte
}
