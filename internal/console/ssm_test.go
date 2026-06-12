package console

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSSMAgentMessageRoundTrip exercises encode/decodeSSMAgentMessage without
// a live SSM data channel — ECS live-testing requires an applied task, so this
// is the deterministic coverage for the AgentMessage wire format itself.
func TestSSMAgentMessageRoundTrip(t *testing.T) {
	want := ssmAgentMessage{
		MessageType:    ssmMsgOutputStreamData,
		SchemaVersion:  ssmSchemaVersion,
		CreatedDate:    uint64(time.Now().UnixMilli()),
		SequenceNumber: 42,
		Flags:          3,
		MessageId:      uuid.New(),
		PayloadType:    ssmPayloadOutput,
		Payload:        []byte("hello from the agent"),
	}

	got, err := decodeSSMAgentMessage(want.encode())
	if err != nil {
		t.Fatalf("decodeSSMAgentMessage: %v", err)
	}

	if got.MessageType != want.MessageType {
		t.Errorf("MessageType = %q, want %q", got.MessageType, want.MessageType)
	}
	if got.SchemaVersion != want.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, want.SchemaVersion)
	}
	if got.CreatedDate != want.CreatedDate {
		t.Errorf("CreatedDate = %d, want %d", got.CreatedDate, want.CreatedDate)
	}
	if got.SequenceNumber != want.SequenceNumber {
		t.Errorf("SequenceNumber = %d, want %d", got.SequenceNumber, want.SequenceNumber)
	}
	if got.Flags != want.Flags {
		t.Errorf("Flags = %d, want %d", got.Flags, want.Flags)
	}
	if got.MessageId != want.MessageId {
		t.Errorf("MessageId = %s, want %s", got.MessageId, want.MessageId)
	}
	if got.PayloadType != want.PayloadType {
		t.Errorf("PayloadType = %d, want %d", got.PayloadType, want.PayloadType)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("Payload = %q, want %q", got.Payload, want.Payload)
	}
}

// TestSSMAgentMessageEmptyPayload covers the zero-length payload case (e.g.
// channel_closed messages), which exercises the boundary at
// ssmHeaderFixedLength+0.
func TestSSMAgentMessageEmptyPayload(t *testing.T) {
	want := ssmAgentMessage{
		MessageType:   ssmMsgChannelClosed,
		SchemaVersion: ssmSchemaVersion,
		MessageId:     uuid.New(),
	}

	got, err := decodeSSMAgentMessage(want.encode())
	if err != nil {
		t.Fatalf("decodeSSMAgentMessage: %v", err)
	}
	if got.MessageType != ssmMsgChannelClosed {
		t.Errorf("MessageType = %q, want %q", got.MessageType, ssmMsgChannelClosed)
	}
	if len(got.Payload) != 0 {
		t.Errorf("Payload = %q, want empty", got.Payload)
	}
}

// TestDecodeSSMAgentMessageTruncated covers the two corruption guards in
// decodeSSMAgentMessage: a frame shorter than the fixed header, and a
// PayloadLength that overruns the buffer.
func TestDecodeSSMAgentMessageTruncated(t *testing.T) {
	if _, err := decodeSSMAgentMessage(make([]byte, ssmHeaderFixedLength-1)); err == nil {
		t.Fatal("expected error for frame shorter than fixed header")
	}

	full := ssmAgentMessage{MessageType: ssmMsgAcknowledge, Payload: []byte("abc")}.encode()
	if _, err := decodeSSMAgentMessage(full[:len(full)-1]); err == nil {
		t.Fatal("expected error for payload length exceeding frame size")
	}
}
