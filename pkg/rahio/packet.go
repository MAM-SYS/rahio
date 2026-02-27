package rahio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
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

// packetTypeName returns a human-readable name for a packet type byte.
func packetTypeName(t uint8) string {
	switch t {
	case TypeData:
		return "DATA"
	case TypeHandshake:
		return "HANDSHAKE"
	case TypeHandshakeAck:
		return "HANDSHAKE_ACK"
	case TypeAck:
		return "ACK"
	case TypeClose:
		return "CLOSE"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	default:
		return fmt.Sprintf("UNKNOWN(0x%02x)", t)
	}
}

// flagsStr returns a human-readable representation of packet flags.
func flagsStr(flags uint8) string {
	if flags == 0 {
		return "none"
	}
	s := ""
	if flags&FlagFIN != 0 {
		s += "FIN|"
	}
	if flags&FlagRST != 0 {
		s += "RST|"
	}
	if flags&FlagScheduled != 0 {
		s += "SCHEDULED|"
	}
	return s[:len(s)-1] // trim trailing |
}

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
	wireTotal := HeaderSize + p.DataLength

	slog.Debug("WritePacket: serializing",
		"type", packetTypeName(p.Type),
		"sfIdx", p.SubflowIndex,
		"seq", p.SequenceNumber,
		"dataLen", p.DataLength,
		"wireTotal", wireTotal,
		"flags", flagsStr(p.Flags),
		"connID", connIDStr(p.ConnectionID),
	)

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

	slog.Debug("WritePacket: checksum computed",
		"type", packetTypeName(p.Type),
		"seq", p.SequenceNumber,
		"checksum", fmt.Sprintf("0x%08x", p.Checksum),
	)

	// Write Framing Length (4 bytes) + Header + Data
	if err := binary.Write(w, binary.BigEndian, wireTotal); err != nil {
		slog.Error("WritePacket: failed to write frame length",
			"type", packetTypeName(p.Type),
			"seq", p.SequenceNumber,
			"err", err,
		)
		return err
	}

	if _, err := w.Write(headers); err != nil {
		slog.Error("WritePacket: failed to write header",
			"type", packetTypeName(p.Type),
			"seq", p.SequenceNumber,
			"err", err,
		)
		return err
	}

	if p.DataLength > 0 {
		if _, err := w.Write(p.Data); err != nil {
			slog.Error("WritePacket: failed to write data",
				"type", packetTypeName(p.Type),
				"seq", p.SequenceNumber,
				"dataLen", p.DataLength,
				"err", err,
			)
			return err
		}
	}

	slog.Debug("WritePacket: done",
		"type", packetTypeName(p.Type),
		"seq", p.SequenceNumber,
		"wireTotal", wireTotal,
	)
	return nil
}

// ReadPacket parses a packet using the length prefix framing.
func ReadPacket(r io.Reader) (*Packet, error) {
	var wireTotal uint32
	if err := binary.Read(r, binary.BigEndian, &wireTotal); err != nil {
		slog.Debug("ReadPacket: failed to read frame length", "err", err)
		return nil, err
	}

	slog.Debug("ReadPacket: frame length received", "wireTotal", wireTotal, "minExpected", HeaderSize)

	if wireTotal < HeaderSize {
		slog.Error("ReadPacket: frame too short",
			"wireTotal", wireTotal,
			"minRequired", HeaderSize,
		)
		return nil, errors.New("packet too short")
	}

	buf := make([]byte, wireTotal)
	if _, err := io.ReadFull(r, buf); err != nil {
		slog.Error("ReadPacket: failed to read packet body",
			"wireTotal", wireTotal,
			"err", err,
		)
		return nil, err
	}

	pkt := &Packet{
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
	}

	slog.Debug("ReadPacket: parsed",
		"type", packetTypeName(pkt.Type),
		"sfIdx", pkt.SubflowIndex,
		"seq", pkt.SequenceNumber,
		"dataLen", pkt.DataLength,
		"checksum", fmt.Sprintf("0x%08x", pkt.Checksum),
		"flags", flagsStr(pkt.Flags),
		"version", pkt.Version,
		"connID", connIDStr(pkt.ConnectionID),
	)

	if pkt.Version != ProtocolVersion {
		slog.Warn("ReadPacket: unexpected protocol version",
			"got", pkt.Version,
			"expected", ProtocolVersion,
			"type", packetTypeName(pkt.Type),
		)
	}

	return pkt, nil
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
	computed := h.Sum32()
	ok := computed == p.Checksum

	if ok {
		slog.Debug("VerifyChecksum: OK",
			"type", packetTypeName(p.Type),
			"seq", p.SequenceNumber,
			"checksum", fmt.Sprintf("0x%08x", p.Checksum),
		)
	} else {
		slog.Warn("VerifyChecksum: MISMATCH",
			"type", packetTypeName(p.Type),
			"seq", p.SequenceNumber,
			"expected", fmt.Sprintf("0x%08x", p.Checksum),
			"computed", fmt.Sprintf("0x%08x", computed),
			"connID", connIDStr(p.ConnectionID),
		)
	}

	return ok
}
