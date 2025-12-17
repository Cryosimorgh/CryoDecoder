package cryodecoder

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

/*
Wire format:

BOF
uint32 objectCount
[ Object 1 ]
[ Object 2 ]
...
[ Object N ]
EOF

Object format:

uint8  lengthOfLength
N      lengthBytes (big endian)
TLV payload (length bytes)
ObjectEndMarker
*/

var (
	ErrInvalidBOF       = errors.New("invalid BOF marker")
	ErrInvalidEOF       = errors.New("invalid EOF marker")
	ErrInvalidObjectEnd = errors.New("invalid object end marker")
	ErrUnknownFieldTag  = errors.New("unknown field tag")
	ErrMalformedData    = errors.New("malformed data")
)

var (
	BOFMarker       = []byte{0x80, 0x00, 0x00, 0x00}
	EOFMarker       = []byte{0x00, 0x00, 0x00, 0x01}
	ObjectEndMarker = []byte{0x66, 0x66, 0x66, 0x66}
)

type Decoder struct {
	reader io.Reader
	schema map[uint8]Codec
}

func NewDecoder(r io.Reader, schema map[uint8]Codec) *Decoder {
	return &Decoder{
		reader: r,
		schema: schema,
	}
}

/*
DecodeStream decodes the entire stream deterministically.
No speculative reads. No EOF guessing.
*/
func (d *Decoder) DecodeStream() ([]map[uint8]any, error) {
	// BOF
	if err := d.readMarker(BOFMarker, ErrInvalidBOF); err != nil {
		return nil, err
	}

	// Object count
	var objectCount uint32
	if err := binary.Read(d.reader, binary.BigEndian, &objectCount); err != nil {
		return nil, err
	}

	objects := make([]map[uint8]any, 0, objectCount)

	for i := uint32(0); i < objectCount; i++ {
		obj, err := d.DecodeObject()
		if err != nil {
			return nil, fmt.Errorf("object %d: %w", i, err)
		}
		objects = append(objects, obj)
	}

	// EOF
	if err := d.readMarker(EOFMarker, ErrInvalidEOF); err != nil {
		return nil, err
	}

	return objects, nil
}

/*
DecodeObject decodes a single object.
*/
func (d *Decoder) DecodeObject() (map[uint8]any, error) {
	// Length-of-length
	var lenOfLen uint8
	if err := binary.Read(d.reader, binary.BigEndian, &lenOfLen); err != nil {
		return nil, err
	}

	if lenOfLen == 0 || lenOfLen > 8 {
		return nil, ErrMalformedData
	}

	// Length buffer
	lengthBytes := make([]byte, lenOfLen)
	if _, err := io.ReadFull(d.reader, lengthBytes); err != nil {
		return nil, err
	}

	// Interpret length (uint16 / uint32 supported)
	var payloadLen uint64
	switch lenOfLen {
	case 2:
		payloadLen = uint64(binary.BigEndian.Uint16(lengthBytes))
	case 4:
		payloadLen = uint64(binary.BigEndian.Uint32(lengthBytes))
	default:
		return nil, ErrMalformedData
	}

	// Payload
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(d.reader, payload); err != nil {
		return nil, err
	}

	// Parse TLVs
	obj, err := d.parseTLVPayload(payload)
	if err != nil {
		return nil, err
	}

	// Object end marker
	if err := d.readMarker(ObjectEndMarker, ErrInvalidObjectEnd); err != nil {
		return nil, err
	}

	return obj, nil
}

/*
parseTLVPayload parses TLV data using the codec schema.
*/
func (d *Decoder) parseTLVPayload(data []byte) (map[uint8]any, error) {
	result := make(map[uint8]any)
	buf := bytes.NewReader(data)

	for buf.Len() > 0 {
		// Tag
		tag, err := buf.ReadByte()
		if err != nil {
			return nil, ErrMalformedData
		}

		codec, ok := d.schema[tag]
		if !ok {
			return nil, fmt.Errorf("%w: %d", ErrUnknownFieldTag, tag)
		}

		// Length-of-length
		lenOfLen, err := buf.ReadByte()
		if err != nil {
			return nil, ErrMalformedData
		}

		if lenOfLen == 0 || lenOfLen > 8 {
			return nil, ErrMalformedData
		}

		// Length
		lengthBytes := make([]byte, lenOfLen)
		if _, err := io.ReadFull(buf, lengthBytes); err != nil {
			return nil, ErrMalformedData
		}

		var valueLen uint64
		switch lenOfLen {
		case 2:
			valueLen = uint64(binary.BigEndian.Uint16(lengthBytes))
		case 4:
			valueLen = uint64(binary.BigEndian.Uint32(lengthBytes))
		default:
			return nil, ErrMalformedData
		}

		// Value
		valueBytes := make([]byte, valueLen)
		if _, err := io.ReadFull(buf, valueBytes); err != nil {
			return nil, ErrMalformedData
		}

		// Decode via codec
		value, err := codec.Decode(valueBytes)
		if err != nil {
			return nil, err
		}

		result[tag] = value
	}

	return result, nil
}

/*
readMarker reads and validates a fixed marker.
*/
func (d *Decoder) readMarker(expected []byte, err error) error {
	buf := make([]byte, len(expected))
	if _, e := io.ReadFull(d.reader, buf); e != nil {
		return e
	}
	if !bytes.Equal(buf, expected) {
		return err
	}
	return nil
}
