// decoder/decoder.go
package cryodecoder

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// Define custom errors for clearer failure cases.
var (
	ErrInvalidBOF       = errors.New("invalid BOF marker")
	ErrInvalidEOF       = errors.New("invalid EOF marker")
	ErrInvalidObjectEnd = errors.New("invalid object end marker")
	ErrUnknownFieldTag  = errors.New("unknown field tag in schema")
	ErrTypeConversion   = errors.New("failed to convert value to the specified type")
	ErrIncompleteData   = errors.New("reached end of data unexpectedly")
)

// Define the markers as byte slices for easy comparison.
var (
	BOFMarker       = []byte{0x80, 0x00, 0x00, 0x00}
	EOFMarker       = []byte{0x00, 0x00, 0x00, 0x01}
	ObjectEndMarker = []byte{0x66, 0x66, 0x66, 0x66}
)

// Decoder holds the state for our decoding process.
type Decoder struct {
	reader io.Reader
	// The schema maps a field tag (uint8) to the type of data it holds.
	schema map[uint8]reflect.Type
}

// NewDecoder creates a new Decoder instance.
// It takes an io.Reader for the data source and a schema to interpret the data.
func NewDecoder(r io.Reader, schema map[uint8]reflect.Type) *Decoder {
	return &Decoder{
		reader: r,
		schema: schema,
	}
}

// DecodeStream reads the entire stream and decodes all objects within it.
func (d *Decoder) DecodeStream() ([]map[uint8]any, error) {
	var allObjects []map[uint8]any

	if err := d.validateMarker(BOFMarker, ErrInvalidBOF); err != nil {
		return nil, err
	}

	for {
		// *** FIX STARTS HERE ***
		// Before trying to read the next object, we must check if we are at the end of the stream.
		// We do this by "peeking" ahead at the next few bytes without consuming them permanently.
		// A simple way to do this is to read the bytes, check them, and if they are not the EOF marker,
		// put them back into the reader for the next operation to consume.

		// Read 4 bytes (the length of the EOF marker) to check what's next.
		var peekBytes = make([]byte, len(EOFMarker))
		n, err := io.ReadFull(d.reader, peekBytes)

		// If we hit an error while peeking, it means the stream is corrupted or ended unexpectedly.
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, err
		}

		// If we successfully read 4 bytes and they match the EOF marker, we are done.
		if n == len(EOFMarker) && bytes.Equal(peekBytes, EOFMarker) {
			// Successfully found the EOF marker, decoding is complete.
			return allObjects, nil
		}

		// If it's not the EOF marker, we need to put the bytes we just read back into the stream.
		// We can do this by creating a new reader that first serves the "peeked" bytes
		// and then continues with the original reader.
		d.reader = io.MultiReader(bytes.NewReader(peekBytes[:n]), d.reader)
		// *** FIX ENDS HERE ***

		// Now it's safe to decode the object, as we've already handled the EOF case.
		object, err := d.DecodeObject()
		if err != nil {
			// If an error occurs during DecodeObject (e.g., corrupted data), return it.
			return nil, err
		}
		allObjects = append(allObjects, object)
	}
}

// DecodeObject reads a single object from the stream.
func (d *Decoder) DecodeObject() (map[uint8]any, error) {
	// 1. Read the "length of length buffer" (1 byte)
	var lengthOfLengthBuffer uint8
	if err := binary.Read(d.reader, binary.BigEndian, &lengthOfLengthBuffer); err != nil {
		return nil, err // This will be io.EOF if the stream ends here.
	}

	// 2. Read the "length buffer" itself
	lengthBytes := make([]byte, lengthOfLengthBuffer)
	if _, err := io.ReadFull(d.reader, lengthBytes); err != nil {
		return nil, fmt.Errorf("%w: while reading length buffer", err)
	}

	// 3. Interpret the "length buffer" as the object's data length
	var objectDataLength uint16
	if err := binary.Read(bytes.NewReader(lengthBytes), binary.BigEndian, &objectDataLength); err != nil {
		return nil, fmt.Errorf("%w: while parsing object data length", err)
	}

	// 4. Read the object data
	objectData := make([]byte, objectDataLength)
	if _, err := io.ReadFull(d.reader, objectData); err != nil {
		return nil, fmt.Errorf("%w: while reading object data", err)
	}

	// 5. Parse the TLV data within the object
	parsedObject, err := d.parseTLVData(objectData)
	if err != nil {
		return nil, err
	}

	// 6. Validate the object end marker
	if err := d.validateMarker(ObjectEndMarker, ErrInvalidObjectEnd); err != nil {
		return nil, err
	}

	return parsedObject, nil
}

// parseTLVData iterates through the object's byte slice and decodes TLVs.
func (d *Decoder) parseTLVData(data []byte) (map[uint8]any, error) {
	result := make(map[uint8]any)
	buf := bytes.NewReader(data)

	for buf.Len() > 0 {
		// Read Tag (1 byte)
		var tag uint8
		if err := binary.Read(buf, binary.BigEndian, &tag); err != nil {
			return nil, fmt.Errorf("%w: while reading tag", ErrIncompleteData)
		}

		// Read "Length of Length" (1 byte)
		var lenOfLen uint8
		if err := binary.Read(buf, binary.BigEndian, &lenOfLen); err != nil {
			return nil, fmt.Errorf("%w: while reading length-of-length", ErrIncompleteData)
		}

		// Read Length (N bytes)
		lengthBytes := make([]byte, lenOfLen)
		if _, err := io.ReadFull(buf, lengthBytes); err != nil {
			return nil, fmt.Errorf("%w: while reading length", ErrIncompleteData)
		}
		var length uint16
		if err := binary.Read(bytes.NewReader(lengthBytes), binary.BigEndian, &length); err != nil {
			return nil, fmt.Errorf("%w: could not parse length", ErrIncompleteData)
		}

		// Read Value
		valueBytes := make([]byte, length)
		if _, err := io.ReadFull(buf, valueBytes); err != nil {
			return nil, fmt.Errorf("%w: while reading value", ErrIncompleteData)
		}

		// Decode value based on schema
		fieldType, ok := d.schema[tag]
		if !ok {
			return nil, fmt.Errorf("%w: %d", ErrUnknownFieldTag, tag)
		}

		decodedValue, err := d.decodeValue(valueBytes, fieldType)
		if err != nil {
			return nil, fmt.Errorf("%w for tag %d", err, tag)
		}

		result[tag] = decodedValue
	}

	return result, nil
}

// decodeValue converts a byte slice to a specific Go type.
func (d *Decoder) decodeValue(data []byte, targetType reflect.Type) (any, error) {
	buf := bytes.NewReader(data)
	switch targetType.Kind() {
	case reflect.String:
		return string(data), nil
	case reflect.Int32:
		var val int32
		err := binary.Read(buf, binary.BigEndian, &val)
		return val, err
	case reflect.Float64:
		var val float64
		err := binary.Read(buf, binary.BigEndian, &val)
		return val, err
	default:
		return nil, fmt.Errorf("%w: %s", ErrTypeConversion, targetType.Kind())
	}
}

// validateMarker reads a number of bytes from the reader and checks them against an expected marker.
func (d *Decoder) validateMarker(expectedMarker []byte, customErr error) error {
	marker := make([]byte, len(expectedMarker))
	if _, err := io.ReadFull(d.reader, marker); err != nil {
		return err
	}
	if !bytes.Equal(marker, expectedMarker) {
		return customErr
	}
	return nil
}
