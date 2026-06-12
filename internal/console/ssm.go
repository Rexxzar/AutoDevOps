package console

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/websocket"
)

// ssmHeaderFixedLength is the fixed-size header of an AgentMessage frame
// (everything before Payload). See ssmAgentMessage for the field layout —
// this is the wire format used by the SSM Session Manager data channel
// (github.com/aws/session-manager-plugin's message.ClientMessage), which
// ECS ExecuteCommand's StreamUrl speaks after the initial token handshake.
const ssmHeaderFixedLength = 120

const ssmMessageTypeLength = 32

const ssmSchemaVersion = 1

// ssm AgentMessage.MessageType values (space-padded to 32 bytes on the wire).
const (
	ssmMsgInputStreamData  = "input_stream_data"
	ssmMsgOutputStreamData = "output_stream_data"
	ssmMsgAcknowledge      = "acknowledge"
	ssmMsgChannelClosed    = "channel_closed"
)

// ssm AgentMessage.PayloadType values.
const (
	ssmPayloadOutput            = uint32(1)
	ssmPayloadError             = uint32(2)
	ssmPayloadSize              = uint32(3)
	ssmPayloadHandshakeRequest  = uint32(5)
	ssmPayloadHandshakeResponse = uint32(6)
	ssmPayloadStdErr            = uint32(11)
)

// ssmAgentMessage is one binary frame of the SSM data channel protocol:
//
//	0   HeaderLength    uint32 BE (= ssmHeaderFixedLength-4, i.e. 116)
//	4   MessageType     32 bytes, space-padded ASCII
//	36  SchemaVersion   uint32 BE
//	40  CreatedDate     uint64 BE (epoch millis)
//	48  SequenceNumber  int64 BE
//	56  Flags           uint64 BE
//	64  MessageId       16 bytes (UUID)
//	80  PayloadDigest   32 bytes (SHA-256 of Payload)
//	112 PayloadType     uint32 BE
//	116 PayloadLength   uint32 BE
//	120 Payload         PayloadLength bytes
type ssmAgentMessage struct {
	MessageType    string
	SchemaVersion  uint32
	CreatedDate    uint64
	SequenceNumber int64
	Flags          uint64
	MessageId      uuid.UUID
	PayloadType    uint32
	Payload        []byte
}

func (m ssmAgentMessage) encode() []byte {
	buf := make([]byte, ssmHeaderFixedLength+len(m.Payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(ssmHeaderFixedLength-4))
	copy(buf[4:4+ssmMessageTypeLength], []byte(fmt.Sprintf("%-32s", m.MessageType)))
	binary.BigEndian.PutUint32(buf[36:40], m.SchemaVersion)
	binary.BigEndian.PutUint64(buf[40:48], m.CreatedDate)
	binary.BigEndian.PutUint64(buf[48:56], uint64(m.SequenceNumber))
	binary.BigEndian.PutUint64(buf[56:64], m.Flags)
	copy(buf[64:80], m.MessageId[:])
	digest := sha256.Sum256(m.Payload)
	copy(buf[80:112], digest[:])
	binary.BigEndian.PutUint32(buf[112:116], m.PayloadType)
	binary.BigEndian.PutUint32(buf[116:120], uint32(len(m.Payload)))
	copy(buf[120:], m.Payload)
	return buf
}

func decodeSSMAgentMessage(b []byte) (ssmAgentMessage, error) {
	if len(b) < ssmHeaderFixedLength {
		return ssmAgentMessage{}, fmt.Errorf("console: ssm frame too short (%d bytes)", len(b))
	}
	var m ssmAgentMessage
	m.MessageType = strings.TrimRight(string(b[4:4+ssmMessageTypeLength]), " ")
	m.SchemaVersion = binary.BigEndian.Uint32(b[36:40])
	m.CreatedDate = binary.BigEndian.Uint64(b[40:48])
	m.SequenceNumber = int64(binary.BigEndian.Uint64(b[48:56]))
	m.Flags = binary.BigEndian.Uint64(b[56:64])
	copy(m.MessageId[:], b[64:80])
	m.PayloadType = binary.BigEndian.Uint32(b[112:116])
	payloadLen := int(binary.BigEndian.Uint32(b[116:120]))
	if payloadLen < 0 || ssmHeaderFixedLength+payloadLen > len(b) {
		return ssmAgentMessage{}, fmt.Errorf("console: ssm frame payload length %d exceeds frame size %d", payloadLen, len(b))
	}
	m.Payload = b[ssmHeaderFixedLength : ssmHeaderFixedLength+payloadLen]
	return m, nil
}

// ssmExecOrigin is a placeholder Origin for the outbound websocket.Dial — the
// SSM data channel doesn't enforce browser-style Origin checks, but
// x/net/websocket requires a syntactically valid URL.
const ssmExecOrigin = "http://localhost/"

// ssmSession is a Session backed by the SSM Session Manager data channel
// (ECS ExecuteCommand). It speaks the AgentMessage binary protocol: sends the
// token handshake, replies to HandshakeRequest, acknowledges
// output_stream_data frames, and encodes Write/Resize as input_stream_data —
// so the Manager/API layer see a plain byte stream via Read/Write.
type ssmSession struct {
	conn    *websocket.Conn
	dataSeq int64
	raw     bytes.Buffer // undecoded bytes read from conn, pending frame assembly
	out     bytes.Buffer // decoded Output/StdErr payload pending Read

	// seenOutput tracks MessageIds of output_stream_data frames already
	// processed. The agent retransmits the same frame (identical MessageId)
	// every ~150ms until acked-and-settled or channel_closed — without this,
	// Read would re-deliver the same payload many times over.
	seenOutput map[uuid.UUID]struct{}
}

// ssmOpenDataChannelInput is the JSON envelope sent as the first text frame
// after the websocket connects, per session-manager-plugin's
// DataChannel.Open (message.OpenDataChannelInput) — the agent rejects a bare
// token string and closes the connection without it.
type ssmOpenDataChannelInput struct {
	MessageSchemaVersion string `json:"MessageSchemaVersion"`
	RequestID            string `json:"RequestId"`
	TokenValue           string `json:"TokenValue"`
	ClientID             string `json:"ClientId"`
}

// dialSSMSession connects to streamURL and performs the initial
// OpenDataChannelInput handshake (sent as a JSON text frame) before switching
// to binary AgentMessage frames.
func dialSSMSession(streamURL, token string) (*ssmSession, error) {
	conn, err := websocket.Dial(streamURL, "", ssmExecOrigin)
	if err != nil {
		return nil, fmt.Errorf("console: ssm websocket dial: %w", err)
	}
	open := ssmOpenDataChannelInput{
		MessageSchemaVersion: "1.0",
		RequestID:            uuid.New().String(),
		TokenValue:           token,
		ClientID:             uuid.New().String(),
	}
	payload, err := json.Marshal(open)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("console: encode ssm open data channel: %w", err)
	}
	conn.PayloadType = websocket.TextFrame
	if _, err := conn.Write(payload); err != nil {
		conn.Close()
		return nil, fmt.Errorf("console: ssm token handshake: %w", err)
	}
	conn.PayloadType = websocket.BinaryFrame
	return &ssmSession{conn: conn, seenOutput: make(map[uuid.UUID]struct{})}, nil
}

// fill reads more raw bytes from the websocket into s.raw.
func (s *ssmSession) fill() error {
	buf := make([]byte, 32*1024)
	n, err := s.conn.Read(buf)
	if n > 0 {
		s.raw.Write(buf[:n])
	}
	return err
}

// nextFrame decodes and consumes one AgentMessage from s.raw, reading more
// from the websocket as needed — x/net/websocket.Conn.Read doesn't guarantee
// frame-aligned reads, so frames are reassembled from a byte stream using
// each frame's own PayloadLength.
func (s *ssmSession) nextFrame() (ssmAgentMessage, error) {
	for {
		b := s.raw.Bytes()
		if len(b) >= ssmHeaderFixedLength {
			payloadLen := int(binary.BigEndian.Uint32(b[116:120]))
			total := ssmHeaderFixedLength + payloadLen
			if len(b) >= total {
				frame := make([]byte, total)
				copy(frame, b[:total])
				s.raw.Next(total)
				msg, err := decodeSSMAgentMessage(frame)
				return msg, err
			}
		}
		if err := s.fill(); err != nil {
			return ssmAgentMessage{}, err
		}
	}
}

func (s *ssmSession) send(msg ssmAgentMessage) error {
	_, err := s.conn.Write(msg.encode())
	return err
}

// ack acknowledges a received output_stream_data frame so the agent doesn't
// retransmit it.
func (s *ssmSession) ack(msg ssmAgentMessage) error {
	payload, err := json.Marshal(map[string]any{
		"AcknowledgedMessageType":           msg.MessageType,
		"AcknowledgedMessageId":             msg.MessageId.String(),
		"AcknowledgedMessageSequenceNumber": msg.SequenceNumber,
		"IsSequentialMessage":               true,
	})
	if err != nil {
		return fmt.Errorf("console: encode ssm ack: %w", err)
	}
	return s.send(ssmAgentMessage{
		MessageType:   ssmMsgAcknowledge,
		SchemaVersion: ssmSchemaVersion,
		CreatedDate:   uint64(time.Now().UnixMilli()),
		MessageId:     uuid.New(),
		Payload:       payload,
	})
}

// sendHandshakeResponse replies to the agent's HandshakeRequest, reporting
// success for whatever session type it asked for — this client doesn't
// negotiate encryption or session-type-specific properties.
func (s *ssmSession) sendHandshakeResponse() error {
	payload, err := json.Marshal(map[string]any{
		"ClientVersion": "1.0.0",
		"ProcessedClientActions": []map[string]any{
			{"ActionType": "SessionType", "ActionStatus": 0},
		},
	})
	if err != nil {
		return fmt.Errorf("console: encode ssm handshake response: %w", err)
	}
	return s.send(ssmAgentMessage{
		MessageType:   ssmMsgInputStreamData,
		SchemaVersion: ssmSchemaVersion,
		CreatedDate:   uint64(time.Now().UnixMilli()),
		MessageId:     uuid.New(),
		PayloadType:   ssmPayloadHandshakeResponse,
		Payload:       payload,
	})
}

// Read decodes AgentMessage frames until output (Output/StdErr/Error payload)
// is available, replying to handshake requests and acknowledging output
// frames as it goes. Returns io.EOF on channel_closed.
func (s *ssmSession) Read(p []byte) (int, error) {
	for s.out.Len() == 0 {
		msg, err := s.nextFrame()
		if err != nil {
			return 0, err
		}
		switch msg.MessageType {
		case ssmMsgOutputStreamData:
			if err := s.ack(msg); err != nil {
				return 0, err
			}
			if _, dup := s.seenOutput[msg.MessageId]; dup {
				continue // retransmission of an already-processed frame
			}
			s.seenOutput[msg.MessageId] = struct{}{}
			switch msg.PayloadType {
			case ssmPayloadOutput, ssmPayloadStdErr, ssmPayloadError:
				s.out.Write(msg.Payload)
			case ssmPayloadHandshakeRequest:
				if err := s.sendHandshakeResponse(); err != nil {
					return 0, err
				}
			}
		case ssmMsgChannelClosed:
			return 0, io.EOF
		}
	}
	return s.out.Read(p)
}

// Write sends p as terminal input (input_stream_data, PayloadType=Output).
func (s *ssmSession) Write(p []byte) (int, error) {
	msg := ssmAgentMessage{
		MessageType:    ssmMsgInputStreamData,
		SchemaVersion:  ssmSchemaVersion,
		CreatedDate:    uint64(time.Now().UnixMilli()),
		SequenceNumber: s.dataSeq,
		MessageId:      uuid.New(),
		PayloadType:    ssmPayloadOutput,
		Payload:        p,
	}
	if err := s.send(msg); err != nil {
		return 0, err
	}
	s.dataSeq++
	return len(p), nil
}

// Resize sends a terminal resize (input_stream_data, PayloadType=Size).
func (s *ssmSession) Resize(rows, cols uint16) error {
	payload, err := json.Marshal(map[string]uint16{"cols": cols, "rows": rows})
	if err != nil {
		return fmt.Errorf("console: encode ssm resize: %w", err)
	}
	msg := ssmAgentMessage{
		MessageType:    ssmMsgInputStreamData,
		SchemaVersion:  ssmSchemaVersion,
		CreatedDate:    uint64(time.Now().UnixMilli()),
		SequenceNumber: s.dataSeq,
		MessageId:      uuid.New(),
		PayloadType:    ssmPayloadSize,
		Payload:        payload,
	}
	if err := s.send(msg); err != nil {
		return err
	}
	s.dataSeq++
	return nil
}

func (s *ssmSession) Close() error {
	return s.conn.Close()
}
