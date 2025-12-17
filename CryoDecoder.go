// Package cryodecoder provides a high-performance, type-safe, extensible
// binary encoding/decoding system using a TLV (Tag-Length-Value) format.
//
// It is designed for scenarios requiring low-latency serialization, such as
// real-time networking or structured data persistence. The system is
// schema-driven through a CodecRegistry, which maps type tags to specific
// encoding/decoding logic (Codecs).
//
// Usage is intended to be straightforward, similar to standard JSON packages:
// 1. Create a CodecRegistry.
// 2. Register built-in or custom Codecs for your data types.
// 3. Create an Encoder or Decoder with the registry.
// 4. Encode or decode your data.
package cryodecoder

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"reflect"
)

// BOF and EOF are markers that frame a complete object in the binary stream.
const (
	BOF = 0xAB // Beginning of Frame
	EOF = 0xCD // End of Frame
)

// Codec defines the interface for any type that can encode and decode a specific data type.
// Implementations must be able to serialize a Go value to a byte slice and deserialize it back.
type Codec interface {
	Encode(value interface{}) ([]byte, error)
	Decode(data []byte) (interface{}, error)
}

// CodecRegistry maps a single-byte tag to a Codec implementation and a Go type.
// It serves as the central schema for the serialization process, ensuring type safety.
type CodecRegistry struct {
	codecs map[byte]Codec
	types  map[reflect.Type]byte
}

// NewCodecRegistry creates and returns an empty CodecRegistry, ready for
// type registrations.
func NewCodecRegistry() *CodecRegistry {
	return &CodecRegistry{
		codecs: make(map[byte]Codec),
		types:  make(map[reflect.Type]byte),
	}
}

// RegisterCodec associates a tag with a Codec and a Go type.
// The exampleType is used to determine the Go type for future encoding operations.
// Example: registry.RegisterCodec(1, &Int32Codec{}, int32(0))
func (r *CodecRegistry) RegisterCodec(tag byte, codec Codec, exampleType interface{}) {
	r.codecs[tag] = codec
	r.types[reflect.TypeOf(exampleType)] = tag
}

// GetCodec retrieves the Codec associated with a given tag.
// Returns an error if no codec is registered for the tag.
func (r *CodecRegistry) GetCodec(tag byte) (Codec, error) {
	codec, exists := r.codecs[tag]
	if !exists {
		return nil, fmt.Errorf("no codec registered for tag %d", tag)
	}
	return codec, nil
}

// GetTag retrieves the tag associated with a given value's type.
// Returns an error if the value's type has not been registered.
func (r *CodecRegistry) GetTag(value interface{}) (byte, error) {
	tag, exists := r.types[reflect.TypeOf(value)]
	if !exists {
		return 0, fmt.Errorf("no tag registered for type %T", value)
	}
	return tag, nil
}

// Encoder handles the serialization of Go objects into the CryoDecoder binary format.
type Encoder struct {
	registry *CodecRegistry
	buffer   *bytes.Buffer
}

// NewEncoder creates a new Encoder instance with the provided CodecRegistry.
func NewEncoder(registry *CodecRegistry) *Encoder {
	return &Encoder{
		registry: registry,
		buffer:   &bytes.Buffer{},
	}
}

// Encode serializes a single Go value into the binary TLV format.
// The output includes BOF/EOF markers for robust stream parsing.
func (e *Encoder) Encode(value interface{}) ([]byte, error) {
	e.buffer.Reset() // Ensure buffer is clean for a new encoding operation

	// 1. Write BOF marker
	if err := e.buffer.WriteByte(BOF); err != nil {
		return nil, fmt.Errorf("failed to write BOF marker: %w", err)
	}

	// 2. Get the tag for the value's type
	tag, err := e.registry.GetTag(value)
	if err != nil {
		return nil, fmt.Errorf("encoding failed: %w", err)
	}

	// 3. Get the codec for the tag
	codec, err := e.registry.GetCodec(tag)
	if err != nil {
		return nil, fmt.Errorf("encoding failed: %w", err)
	}

	// 4. Encode the value payload
	payload, err := codec.Encode(value)
	if err != nil {
		return nil, fmt.Errorf("encoding failed for tag %d: %w", tag, err)
	}

	// 5. Write the TLV header
	if err := e.buffer.WriteByte(tag); err != nil {
		return nil, err
	}

	// Length-of-length is fixed at 2 bytes for this implementation
	lengthOfLength := byte(2)
	if err := e.buffer.WriteByte(lengthOfLength); err != nil {
		return nil, err
	}

	// Payload length
	length := uint16(len(payload))
	if err := binary.Write(e.buffer, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("failed to write payload length: %w", err)
	}

	// 6. Write the payload
	if _, err := e.buffer.Write(payload); err != nil {
		return nil, fmt.Errorf("failed to write payload: %w", err)
	}

	// 7. Write EOF marker
	if err := e.buffer.WriteByte(EOF); err != nil {
		return nil, fmt.Errorf("failed to write EOF marker: %w", err)
	}

	// Return a copy of the buffer's contents
	result := make([]byte, e.buffer.Len())
	copy(result, e.buffer.Bytes())
	return result, nil
}

// Decoder handles the deserialization of a binary stream into Go objects.
type Decoder struct {
	registry *CodecRegistry
	reader   io.Reader
}

// NewDecoder creates a new Decoder instance with the provided CodecRegistry and an io.Reader.
func NewDecoder(registry *CodecRegistry, reader io.Reader) *Decoder {
	return &Decoder{
		registry: registry,
		reader:   reader,
	}
}

// Decode reads a single object from the binary stream, validates its framing,
// and reconstructs the original Go value.
func (d *Decoder) Decode() (interface{}, error) {
	// 1. Validate BOF marker
	if err := d.readMarker(BOF, "BOF"); err != nil {
		return nil, err
	}

	// 2. Read the tag
	tag, err := d.readByte()
	if err != nil {
		return nil, fmt.Errorf("failed to read tag: %w", err)
	}

	// 3. Read length-of-length
	lol, err := d.readByte()
	if err != nil {
		return nil, fmt.Errorf("failed to read length-of-length: %w", err)
	}

	// 4. Read the payload length
	lengthBytes := make([]byte, lol)
	if _, err := io.ReadFull(d.reader, lengthBytes); err != nil {
		return nil, fmt.Errorf("failed to read length bytes: %w", err)
	}
	length := binary.BigEndian.Uint16(lengthBytes)

	// 5. Read the payload
	payload := make([]byte, length)
	if _, err := io.ReadFull(d.reader, payload); err != nil {
		return nil, fmt.Errorf("failed to read payload: %w", err)
	}

	// 6. Get the codec and decode the payload
	codec, err := d.registry.GetCodec(tag)
	if err != nil {
		return nil, fmt.Errorf("decoding failed: %w", err)
	}
	value, err := codec.Decode(payload)
	if err != nil {
		return nil, fmt.Errorf("decoding failed for tag %d: %w", tag, err)
	}

	// 7. Validate EOF marker
	if err := d.readMarker(EOF, "EOF"); err != nil {
		return nil, err
	}

	return value, nil
}

// readMarker is a helper to read and validate a single-byte marker.
func (d *Decoder) readMarker(expected byte, name string) error {
	marker, err := d.readByte()
	if err != nil {
		return fmt.Errorf("failed to read %s marker: %w", name, err)
	}
	if marker != expected {
		return fmt.Errorf("invalid %s marker: expected 0x%X, got 0x%X", name, expected, marker)
	}
	return nil
}

// readByte is a helper to read a single byte from the reader.
func (d *Decoder) readByte() (byte, error) {
	b := make([]byte, 1)
	_, err := io.ReadFull(d.reader, b)
	return b[0], err
}

// --- Primitive Codec Implementations ---

// Int32Codec provides encoding/decoding for Go's int32 type.
type Int32Codec struct{}

func (c *Int32Codec) Encode(value interface{}) ([]byte, error) {
	intVal, ok := value.(int32)
	if !ok {
		return nil, fmt.Errorf("value %v is not int32", value)
	}
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, uint32(intVal))
	return result, nil
}

func (c *Int32Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("invalid data length for int32: expected 4, got %d", len(data))
	}
	return int32(binary.BigEndian.Uint32(data)), nil
}

// Float64Codec provides encoding/decoding for Go's float64 type.
type Float64Codec struct{}

func (c *Float64Codec) Encode(value interface{}) ([]byte, error) {
	floatVal, ok := value.(float64)
	if !ok {
		return nil, fmt.Errorf("value %v is not float64", value)
	}
	bits := math.Float64bits(floatVal)
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, bits)
	return result, nil
}

func (c *Float64Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for float64: expected 8, got %d", len(data))
	}
	bits := binary.BigEndian.Uint64(data)
	return math.Float64frombits(bits), nil
}

// StringCodec provides encoding/decoding for Go's string type.
type StringCodec struct{}

func (c *StringCodec) Encode(value interface{}) ([]byte, error) {
	strVal, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("value %v is not string", value)
	}
	return []byte(strVal), nil
}

func (c *StringCodec) Decode(data []byte) (interface{}, error) {
	return string(data), nil
}

// --- Custom Struct Codec Implementation ---

// StructCodec provides a generic way to encode and decode Go structs.
// It treats a struct as a series of fields, each encoded as its own TLV.
type StructCodec struct {
	registry   *CodecRegistry
	fields     map[string]byte // Maps struct field name to its tag
	fieldTypes map[string]reflect.Type
	structType reflect.Type
}

// NewStructCodec creates a codec for a specific struct type.
// The exampleType must be a struct or a pointer to a struct.
func NewStructCodec(registry *CodecRegistry, exampleType interface{}) *StructCodec {
	structType := reflect.TypeOf(exampleType)
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}
	if structType.Kind() != reflect.Struct {
		// This is a programmer error, so panic is appropriate.
		panic(fmt.Sprintf("NewStructCodec requires a struct or pointer to struct, got %T", exampleType))
	}

	return &StructCodec{
		registry:   registry,
		fields:     make(map[string]byte),
		fieldTypes: make(map[string]reflect.Type),
		structType: structType,
	}
}

// RegisterField maps a struct's field name to a tag, which must have a corresponding
// primitive codec registered in the registry.
func (c *StructCodec) RegisterField(fieldName string, tag byte) {
	// Find the field in the struct type to validate its existence and get its type.
	field, found := c.structType.FieldByName(fieldName)
	if !found {
		// This is a programmer error.
		panic(fmt.Sprintf("field '%s' not found in struct type %v", fieldName, c.structType))
	}

	c.fields[fieldName] = tag
	c.fieldTypes[fieldName] = field.Type
}

func (c *StructCodec) Encode(value interface{}) ([]byte, error) {
	val := reflect.ValueOf(value)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct || val.Type() != c.structType {
		return nil, fmt.Errorf("value %v is not of type %v", value, c.structType)
	}

	var buffer bytes.Buffer

	// Encode each registered field as a nested TLV.
	for fieldName, tag := range c.fields {
		field := val.FieldByName(fieldName)
		if !field.IsValid() {
			return nil, fmt.Errorf("field %s not found in struct value", fieldName)
		}

		// Get the codec for the field's type.
		codec, err := c.registry.GetCodec(tag)
		if err != nil {
			return nil, fmt.Errorf("error getting codec for field %s: %w", fieldName, err)
		}

		// Encode the field value.
		fieldValue := field.Interface()
		encodedValue, err := codec.Encode(fieldValue)
		if err != nil {
			return nil, fmt.Errorf("error encoding field %s: %w", fieldName, err)
		}

		// Write the field's TLV to the buffer.
		buffer.WriteByte(tag)
		buffer.WriteByte(2) // length-of-length
		binary.Write(&buffer, binary.BigEndian, uint16(len(encodedValue)))
		buffer.Write(encodedValue)
	}

	return buffer.Bytes(), nil
}

func (c *StructCodec) Decode(data []byte) (interface{}, error) {
	// Create a new instance of the struct to hold the decoded data.
	result := reflect.New(c.structType).Elem()
	reader := bytes.NewReader(data)

	for reader.Len() > 0 {
		// Read the field's tag.
		var tag byte
		if err := binary.Read(reader, binary.BigEndian, &tag); err != nil {
			return nil, fmt.Errorf("failed to read field tag: %w", err)
		}

		// Read length-of-length and length.
		var lol byte
		if err := binary.Read(reader, binary.BigEndian, &lol); err != nil {
			return nil, fmt.Errorf("failed to read length-of-length: %w", err)
		}
		var length uint16
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return nil, fmt.Errorf("failed to read length: %w", err)
		}

		// Read the field's value payload.
		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, fmt.Errorf("failed to read payload: %w", err)
		}

		// Find the field name associated with this tag.
		var fieldName string
		for name, t := range c.fields {
			if t == tag {
				fieldName = name
				break
			}
		}
		if fieldName == "" {
			// Skip unknown fields to allow for some extensibility.
			continue
		}

		// Get the codec for the field and decode the payload.
		codec, err := c.registry.GetCodec(tag)
		if err != nil {
			return nil, fmt.Errorf("error getting codec for field %s: %w", fieldName, err)
		}
		decodedValue, err := codec.Decode(payload)
		if err != nil {
			return nil, fmt.Errorf("error decoding field %s: %w", fieldName, err)
		}

		// Set the decoded value on the struct instance.
		field := result.FieldByName(fieldName)
		if field.IsValid() && field.CanSet() {
			val := reflect.ValueOf(decodedValue)
			if val.Type().ConvertibleTo(field.Type()) {
				field.Set(val.Convert(field.Type()))
			} else {
				return nil, fmt.Errorf("cannot convert decoded value %v (%v) to field type %v for field %s", decodedValue, val.Type(), field.Type(), fieldName)
			}
		}
	}

	return result.Interface(), nil
}
