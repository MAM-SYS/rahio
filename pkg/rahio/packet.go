package rahio

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

const ProtocolVersion uint8 = 0x01

// Packet Types (Explicit for wire protocol stability)
const (
	TypeData         uint8 = 0x00
	TypeHandshake    uint8 = 0x01
	TypeHandshakeAck uint8 = 0x02
	TypeAck          uint8 = 0x03
	TypeClose        uint8 = 0x04
	TypePing         uint8 = 0x05
	TypePong         uint8 = 0x06
)

// Flags
const (
	FlagFIN       uint8 = 0x01
	FlagRST       uint8 = 0x02
	FlagScheduled uint8 = 0x04
)

const HeaderSize = 48

// Packet perfectly mirrors the 48-byte header specified in Section 5.
type Packet struct {
	Version        uint8
	Type           uint8
	SubflowIndex   uint8
	Flags          uint8
	ConnectionID   [16]byte
	SequenceNumber uint64
	Timestamp      uint64
	DataLength     uint32
	Checksum       uint32
	Data           []byte
}

// WritePacket serializes a packet to an io.Writer with a 4-byte length prefix (Section 5.4).
func WritePacket(w io.Writer, p *Packet) error {
	p.DataLength = uint32(len(p.Data))

	headers := make([]byte, HeaderSize)
	headers[0] = p.Version
	headers[1] = p.Type
	headers[2] = p.SubflowIndex
	headers[3] = p.Flags
	copy(headers[4:20], p.ConnectionID[:])
	binary.BigEndian.PutUint64(headers[20:28], p.SequenceNumber)
	binary.BigEndian.PutUint64(headers[28:36], p.Timestamp)
	binary.BigEndian.PutUint32(headers[36:40], p.DataLength)
	binary.BigEndian.PutUint32(headers[40:44], 0)

	h := crc32.NewIEEE()
	_, _ = h.Write(headers)
	_, _ = h.Write(p.Data)
	p.Checksum = h.Sum32()
	binary.BigEndian.PutUint32(headers[40:44], p.Checksum)

	// Write Framing Length (4 bytes) + Header + Data
	total := HeaderSize + p.DataLength
	if err := binary.Write(w, binary.BigEndian, total); err != nil {
		return err
	}

	if _, err := w.Write(headers); err != nil {
		return err
	}

	if p.DataLength > 0 {
		if _, err := w.Write(p.Data); err != nil {
			return err
		}
	}

	return nil
}

// ReadPacket parses a packet using the length prefix framing.
func ReadPacket(r io.Reader) (*Packet, error) {
	var total uint32
	if err := binary.Read(r, binary.BigEndian, &total); err != nil {
		return nil, err
	}

	if total < HeaderSize {
		return nil, errors.New("packet too short")
	}

	buf := make([]byte, total)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	return &Packet{
		Version:        buf[0],
		Type:           buf[1],
		SubflowIndex:   buf[2],
		Flags:          buf[3],
		ConnectionID:   [16]byte(buf[4:20]),
		SequenceNumber: binary.BigEndian.Uint64(buf[20:28]),
		Timestamp:      binary.BigEndian.Uint64(buf[28:36]),
		DataLength:     binary.BigEndian.Uint32(buf[36:40]),
		Checksum:       binary.BigEndian.Uint32(buf[40:44]),
		Data:           buf[HeaderSize:],
	}, nil
}

// VerifyChecksum recomputes CRC32 over the header (checksum field zeroed) + data.
func VerifyChecksum(p *Packet) bool {
	header := make([]byte, HeaderSize)
	header[0] = p.Version
	header[1] = p.Type
	header[2] = p.SubflowIndex
	header[3] = p.Flags
	copy(header[4:20], p.ConnectionID[:])
	binary.BigEndian.PutUint64(header[20:28], p.SequenceNumber)
	binary.BigEndian.PutUint64(header[28:36], p.Timestamp)
	binary.BigEndian.PutUint32(header[36:40], p.DataLength)
	binary.BigEndian.PutUint32(header[40:44], 0)

	h := crc32.NewIEEE()
	_, _ = h.Write(header)
	_, _ = h.Write(p.Data)

	return h.Sum32() == p.Checksum
}
