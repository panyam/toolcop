package api

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// MaxFrameSize caps a single payload at 1 MiB. Plenty for hook JSON; large
// Edit operations with long new_string fields are the realistic upper bound
// and even those rarely exceed a few KiB.
const MaxFrameSize = 1 << 20

// WriteFrame writes a single length-prefixed payload.
//
// Wire layout: [4-byte big-endian length N][N bytes of payload].
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("toolcop/api: frame too large: %d > %d", len(payload), MaxFrameSize)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// ReadFrame reads a single length-prefixed payload. Returns io.EOF if the
// stream is empty before the header. Returns io.ErrUnexpectedEOF if the
// stream truncates partway through a frame.
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("toolcop/api: frame too large: %d > %d", n, MaxFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteMessage marshals v to JSON and writes it as a single frame.
func WriteMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("toolcop/api: marshal: %w", err)
	}
	return WriteFrame(w, data)
}

// ReadMessage reads a single frame and unmarshals it into v (which must be
// a pointer).
func ReadMessage(r io.Reader, v any) error {
	data, err := ReadFrame(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("toolcop/api: unmarshal: %w", err)
	}
	return nil
}
