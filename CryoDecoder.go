// Package cryodecoder provides a high-performance, type-safe, extensible
// binary encoding/decoding system using a TLV (Tag-Length-Value) format.
//
// The system is designed for simplicity. After registering primitive types,
// complex structs can be registered with a single function call.
package CryoDecoder

import (
	"bytes"
	"encoding"
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
type Codec interface {
	Encode(value interface{}) ([]byte, error)
	Decode(data []byte) (interface{}, error)
}

// CodecRegistry maps a single-byte tag to a Codec implementation and a Go type.
// It handles automatic registration of struct fields via reflection.
type CodecRegistry struct {
	codecs        map[byte]Codec
	types         map[reflect.Type]byte
	nextStructTag byte // To auto-generate unique tags for structs
}

// NewCodecRegistry creates and returns an empty CodecRegistry.
func NewCodecRegistry() *CodecRegistry {
	return &CodecRegistry{
		codecs:        make(map[byte]Codec),
		types:         make(map[reflect.Type]byte, 0),
		nextStructTag: 200, // Reserve tags 0-199 for primitives, start struct tags at 200
	}
}

// RegisterPrimitives is a convenience method to register the built-in primitive codecs.
// It assigns standard tags:
// int32(1), string(2), float64(3), int64(4), bool(5), int(6), int8(7), int16(8),
// uint(9), uint8(10), uint16(11), uint32(12), uint64(13), uintptr(14), float32(15),
// complex64(16), complex128(17), interface{}(18), map[string]interface{}(19).
// Note: Platform-dependent types (int, uint, uintptr) are serialized as int64 or uint64
// for cross-platform compatibility.
func (r *CodecRegistry) RegisterPrimitives() {
	r.RegisterCodec(1, &Int32Codec{}, int32(0))
	r.RegisterCodec(2, &StringCodec{}, "")
	r.RegisterCodec(3, &Float64Codec{}, float64(0))
	r.RegisterCodec(4, &Int64Codec{}, int64(0))
	r.RegisterCodec(5, &BoolCodec{}, false)
	r.RegisterCodec(6, &IntCodec{}, int(0)) // Serialized as int64
	r.RegisterCodec(7, &Int8Codec{}, int8(0))
	r.RegisterCodec(8, &Int16Codec{}, int16(0))
	r.RegisterCodec(9, &UintCodec{}, uint(0))    // Serialized as uint64
	r.RegisterCodec(10, &Uint8Codec{}, uint8(0)) // Also handles byte
	r.RegisterCodec(11, &Uint16Codec{}, uint16(0))
	r.RegisterCodec(12, &Uint32Codec{}, uint32(0))
	r.RegisterCodec(13, &Uint64Codec{}, uint64(0))
	r.RegisterCodec(14, &UintptrCodec{}, uintptr(0)) // Serialized as uint64
	r.RegisterCodec(15, &Float32Codec{}, float32(0))
	r.RegisterCodec(16, &Complex64Codec{}, complex64(0))
	r.RegisterCodec(17, &Complex128Codec{}, complex128(0))
	// NEW: Register dynamic types
	r.RegisterCodec(18, &InterfaceCodec{registry: r}, []any{}) // interface{}
	r.RegisterCodec(19, &MapStringAnyCodec{registry: r}, map[string]any(nil))
}

// RegisterStruct automatically registers a custom struct and all of its nested structs.
// MODIFIED: It now attempts to use BinaryMarshaler for types that aren't registered,
// allowing support for private/built-in structs like time.Time.
func (r *CodecRegistry) RegisterStruct(exampleType interface{}) (byte, error) {
	structType := reflect.TypeOf(exampleType)
	if structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}
	if structType.Kind() != reflect.Struct {
		return 0, fmt.Errorf("RegisterStruct requires a struct or pointer to struct, got %T", exampleType)
	}

	// If the struct is already registered, return its tag.
	if tag, exists := r.types[structType]; exists {
		return tag, nil
	}

	// Create the struct codec
	codec := NewStructCodec(r, exampleType)

	// Iterate through fields and register them
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		fieldType := field.Type

		// Helper to ensure we have a tag for the field type
		getOrRegisterTag := func(t reflect.Type) (byte, error) {
			// Check if already known
			if tag, exists := r.types[t]; exists {
				return tag, nil
			}

			// NEW: Check if it implements BinaryMarshaler.
			// This allows handling private/built-in structs (e.g., time.Time) automatically.
			if t.Implements(reflect.TypeFor[encoding.BinaryMarshaler]()) {
				newTag := r.nextStructTag
				r.nextStructTag++

				// Create a zero value to register the type
				zeroValue := reflect.New(t).Elem().Interface()

				// Register the generic MarshalerCodec
				r.RegisterCodec(newTag, &MarshalerCodec{typ: t}, zeroValue)
				return newTag, nil
			}

			// If it's a struct we don't know, try to register it recursively
			if t.Kind() == reflect.Struct {
				nestedInstance := reflect.New(t).Interface()
				return r.RegisterStruct(nestedInstance)
			}

			return 0, fmt.Errorf("no codec registered for type %v and it is not a struct or BinaryMarshaler", t)
		}

		typeTag, err := getOrRegisterTag(fieldType)
		if err != nil {
			return 0, fmt.Errorf("failed to resolve codec for field '%s' (%v): %w", field.Name, fieldType, err)
		}

		// Register the field with the struct codec.
		codec.RegisterField(field.Name, typeTag)
	}

	// Assign a new tag to the struct and register the codec.
	structTag := r.nextStructTag
	r.nextStructTag++

	// Register using a value instance for consistency.
	valueInstance := reflect.New(structType).Elem().Interface()
	r.RegisterCodec(structTag, codec, valueInstance)

	return structTag, nil
}

// RegisterCodec is a low-level method to associate a tag with a Codec and a Go type.
func (r *CodecRegistry) RegisterCodec(tag byte, codec Codec, exampleType interface{}) {
	r.codecs[tag] = codec
	r.types[reflect.TypeOf(exampleType)] = tag
}

// GetCodec retrieves the Codec associated with a given tag.
func (r *CodecRegistry) GetCodec(tag byte) (Codec, error) {
	codec, exists := r.codecs[tag]
	if !exists {
		return nil, fmt.Errorf("no codec registered for tag %d", tag)
	}
	return codec, nil
}

// GetTag retrieves the tag associated with a given value's type.
func (r *CodecRegistry) GetTag(value interface{}) (byte, error) {
	tag, exists := r.types[reflect.TypeOf(value)]
	if !exists {
		return 0, fmt.Errorf("no tag registered for type %T", value)
	}
	return tag, nil
}

// --- Encoder and Decoder ---

// Encoder handles the serialization of Go objects into the CryoDecoder binary format.
type Encoder struct {
	registry *CodecRegistry
	buffer   *bytes.Buffer
}

// NewEncoder creates a new Encoder instance with the provided CodecRegistry.
func NewEncoder(registry *CodecRegistry) *Encoder {
	return &Encoder{registry: registry, buffer: &bytes.Buffer{}}
}

// Encode serializes a single Go value into the binary TLV format.
func (e *Encoder) Encode(value interface{}) ([]byte, error) {
	e.buffer.Reset()
	if err := e.buffer.WriteByte(BOF); err != nil {
		return nil, fmt.Errorf("failed to write BOF marker: %w", err)
	}

	// NEW: Handle interface{} wrapping dynamically if necessary.
	// If the value is an interface{}, we treat it as an "Any" type,
	// ensuring we capture the concrete type tag.
	val := reflect.ValueOf(value)
	if val.Kind() == reflect.Interface && !val.IsNil() {
		value = val.Elem().Interface()
	}

	tag, err := e.registry.GetTag(value)
	if err != nil {
		return nil, fmt.Errorf("encoding failed: %w", err)
	}
	codec, err := e.registry.GetCodec(tag)
	if err != nil {
		return nil, fmt.Errorf("encoding failed: %w", err)
	}
	payload, err := codec.Encode(value)
	if err != nil {
		return nil, fmt.Errorf("encoding failed for tag %d: %w", tag, err)
	}
	if err := e.buffer.WriteByte(tag); err != nil {
		return nil, err
	}
	e.buffer.WriteByte(2) // length-of-length
	if err := binary.Write(e.buffer, binary.BigEndian, uint16(len(payload))); err != nil {
		return nil, fmt.Errorf("failed to write payload length: %w", err)
	}
	if _, err := e.buffer.Write(payload); err != nil {
		return nil, fmt.Errorf("failed to write payload: %w", err)
	}
	if err := e.buffer.WriteByte(EOF); err != nil {
		return nil, fmt.Errorf("failed to write EOF marker: %w", err)
	}
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
	return &Decoder{registry: registry, reader: reader}
}

// Decode reads a single object from the binary stream.
func (d *Decoder) Decode() (interface{}, error) {
	if err := d.readMarker(BOF, "BOF"); err != nil {
		return nil, err
	}
	tag, err := d.readByte()
	if err != nil {
		return nil, fmt.Errorf("failed to read tag: %w", err)
	}
	lol, err := d.readByte()
	if err != nil {
		return nil, fmt.Errorf("failed to read length-of-length: %w", err)
	}
	lengthBytes := make([]byte, lol)
	if _, err := io.ReadFull(d.reader, lengthBytes); err != nil {
		return nil, fmt.Errorf("failed to read length bytes: %w", err)
	}
	length := binary.BigEndian.Uint16(lengthBytes)
	payload := make([]byte, length)
	if _, err := io.ReadFull(d.reader, payload); err != nil {
		return nil, fmt.Errorf("failed to read payload: %w", err)
	}
	codec, err := d.registry.GetCodec(tag)
	if err != nil {
		return nil, fmt.Errorf("decoding failed: %w", err)
	}
	value, err := codec.Decode(payload)
	if err != nil {
		return nil, fmt.Errorf("decoding failed for tag %d: %w", tag, err)
	}
	if err := d.readMarker(EOF, "EOF"); err != nil {
		return nil, err
	}
	return value, nil
}

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

func (d *Decoder) readByte() (byte, error) {
	b := make([]byte, 1)
	_, err := io.ReadFull(d.reader, b)
	return b[0], err
}

// --- Primitive Codec Implementations ---

// Integer Codecs
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

type Int64Codec struct{}

func (c *Int64Codec) Encode(value interface{}) ([]byte, error) {
	intVal, ok := value.(int64)
	if !ok {
		return nil, fmt.Errorf("value %v is not int64", value)
	}
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, uint64(intVal))
	return result, nil
}

func (c *Int64Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for int64: expected 8, got %d", len(data))
	}
	return int64(binary.BigEndian.Uint64(data)), nil
}

type IntCodec struct{} // Serialized as int64 for compatibility

func (c *IntCodec) Encode(value interface{}) ([]byte, error) {
	intVal, ok := value.(int)
	if !ok {
		return nil, fmt.Errorf("value %v is not int", value)
	}
	int64Val := int64(intVal)
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, uint64(int64Val))
	return result, nil
}

func (c *IntCodec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for int: expected 8, got %d", len(data))
	}
	return int(binary.BigEndian.Uint64(data)), nil
}

type Int8Codec struct{}

func (c *Int8Codec) Encode(value interface{}) ([]byte, error) {
	intVal, ok := value.(int8)
	if !ok {
		return nil, fmt.Errorf("value %v is not int8", value)
	}
	return []byte{byte(intVal)}, nil
}

func (c *Int8Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 1 {
		return nil, fmt.Errorf("invalid data length for int8: expected 1, got %d", len(data))
	}
	return int8(data[0]), nil
}

type Int16Codec struct{}

func (c *Int16Codec) Encode(value interface{}) ([]byte, error) {
	intVal, ok := value.(int16)
	if !ok {
		return nil, fmt.Errorf("value %v is not int16", value)
	}
	result := make([]byte, 2)
	binary.BigEndian.PutUint16(result, uint16(intVal))
	return result, nil
}

func (c *Int16Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 2 {
		return nil, fmt.Errorf("invalid data length for int16: expected 2, got %d", len(data))
	}
	return int16(binary.BigEndian.Uint16(data)), nil
}

// Unsigned Integer Codecs
type Uint8Codec struct{} // Also handles byte

func (c *Uint8Codec) Encode(value interface{}) ([]byte, error) {
	uintVal, ok := value.(uint8)
	if !ok {
		return nil, fmt.Errorf("value %v is not uint8", value)
	}
	return []byte{uintVal}, nil
}

func (c *Uint8Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 1 {
		return nil, fmt.Errorf("invalid data length for uint8: expected 1, got %d", len(data))
	}
	return data[0], nil
}

type Uint16Codec struct{}

func (c *Uint16Codec) Encode(value interface{}) ([]byte, error) {
	uintVal, ok := value.(uint16)
	if !ok {
		return nil, fmt.Errorf("value %v is not uint16", value)
	}
	result := make([]byte, 2)
	binary.BigEndian.PutUint16(result, uintVal)
	return result, nil
}

func (c *Uint16Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 2 {
		return nil, fmt.Errorf("invalid data length for uint16: expected 2, got %d", len(data))
	}
	return binary.BigEndian.Uint16(data), nil
}

type Uint32Codec struct{}

func (c *Uint32Codec) Encode(value interface{}) ([]byte, error) {
	uintVal, ok := value.(uint32)
	if !ok {
		return nil, fmt.Errorf("value %v is not uint32", value)
	}
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, uintVal)
	return result, nil
}

func (c *Uint32Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("invalid data length for uint32: expected 4, got %d", len(data))
	}
	return binary.BigEndian.Uint32(data), nil
}

type Uint64Codec struct{}

func (c *Uint64Codec) Encode(value interface{}) ([]byte, error) {
	uintVal, ok := value.(uint64)
	if !ok {
		return nil, fmt.Errorf("value %v is not uint64", value)
	}
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, uintVal)
	return result, nil
}

func (c *Uint64Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for uint64: expected 8, got %d", len(data))
	}
	return binary.BigEndian.Uint64(data), nil
}

type UintCodec struct{} // Serialized as uint64 for compatibility

func (c *UintCodec) Encode(value interface{}) ([]byte, error) {
	uintVal, ok := value.(uint)
	if !ok {
		return nil, fmt.Errorf("value %v is not uint", value)
	}
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, uint64(uintVal))
	return result, nil
}

func (c *UintCodec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for uint: expected 8, got %d", len(data))
	}
	return uint(binary.BigEndian.Uint64(data)), nil
}

type UintptrCodec struct{} // Serialized as uint64 for compatibility

func (c *UintptrCodec) Encode(value interface{}) ([]byte, error) {
	uintptrVal, ok := value.(uintptr)
	if !ok {
		return nil, fmt.Errorf("value %v is not uintptr", value)
	}
	result := make([]byte, 8)
	binary.BigEndian.PutUint64(result, uint64(uintptrVal))
	return result, nil
}

func (c *UintptrCodec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for uintptr: expected 8, got %d", len(data))
	}
	return uintptr(binary.BigEndian.Uint64(data)), nil
}

// Floating-point Codecs
type Float32Codec struct{}

func (c *Float32Codec) Encode(value interface{}) ([]byte, error) {
	floatVal, ok := value.(float32)
	if !ok {
		return nil, fmt.Errorf("value %v is not float32", value)
	}
	bits := math.Float32bits(floatVal)
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, bits)
	return result, nil
}

func (c *Float32Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 4 {
		return nil, fmt.Errorf("invalid data length for float32: expected 4, got %d", len(data))
	}
	bits := binary.BigEndian.Uint32(data)
	return math.Float32frombits(bits), nil
}

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

// Complex Number Codecs
type Complex64Codec struct{} // Two float32s

func (c *Complex64Codec) Encode(value interface{}) ([]byte, error) {
	complexVal, ok := value.(complex64)
	if !ok {
		return nil, fmt.Errorf("value %v is not complex64", value)
	}
	realCodec := &Float32Codec{}
	imagCodec := &Float32Codec{}
	realBytes, err := realCodec.Encode(real(complexVal))
	if err != nil {
		return nil, err
	}
	imagBytes, err := imagCodec.Encode(imag(complexVal))
	if err != nil {
		return nil, err
	}
	return append(realBytes, imagBytes...), nil
}

func (c *Complex64Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 8 {
		return nil, fmt.Errorf("invalid data length for complex64: expected 8, got %d", len(data))
	}
	realFloat := math.Float32frombits(binary.BigEndian.Uint32(data[:4]))
	imagFloat := math.Float32frombits(binary.BigEndian.Uint32(data[4:]))
	return complex(realFloat, imagFloat), nil
}

type Complex128Codec struct{} // Two float64s

func (c *Complex128Codec) Encode(value interface{}) ([]byte, error) {
	complexVal, ok := value.(complex128)
	if !ok {
		return nil, fmt.Errorf("value %v is not complex128", value)
	}
	realCodec := &Float64Codec{}
	imagCodec := &Float64Codec{}
	realBytes, err := realCodec.Encode(real(complexVal))
	if err != nil {
		return nil, err
	}
	imagBytes, err := imagCodec.Encode(imag(complexVal))
	if err != nil {
		return nil, err
	}
	return append(realBytes, imagBytes...), nil
}

func (c *Complex128Codec) Decode(data []byte) (interface{}, error) {
	if len(data) != 16 {
		return nil, fmt.Errorf("invalid data length for complex128: expected 16, got %d", len(data))
	}
	realFloat := math.Float64frombits(binary.BigEndian.Uint64(data[:8]))
	imagFloat := math.Float64frombits(binary.BigEndian.Uint64(data[8:]))
	return complex(realFloat, imagFloat), nil
}

// Other Primitive Codecs
type BoolCodec struct{}

func (c *BoolCodec) Encode(value interface{}) ([]byte, error) {
	boolVal, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("value %v is not bool", value)
	}
	if boolVal {
		return []byte{1}, nil
	}
	return []byte{0}, nil
}

func (c *BoolCodec) Decode(data []byte) (interface{}, error) {
	if len(data) != 1 {
		return nil, fmt.Errorf("invalid data length for bool: expected 1, got %d", len(data))
	}
	return data[0] == 1, nil
}

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

type StructCodec struct {
	registry   *CodecRegistry
	fields     []fieldInfo
	structType reflect.Type
}

type fieldInfo struct {
	name     string
	typeTag  byte
	typeInfo reflect.Type
}

func NewStructCodec(registry *CodecRegistry, exampleType interface{}) *StructCodec {
	structType := reflect.TypeOf(exampleType)
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}
	if structType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("NewStructCodec requires a struct or pointer to struct, got %T", exampleType))
	}
	return &StructCodec{registry: registry, fields: make([]fieldInfo, 0), structType: structType}
}

func (c *StructCodec) RegisterField(fieldName string, typeTag byte) {
	field, found := c.structType.FieldByName(fieldName)
	if !found {
		panic(fmt.Sprintf("field '%s' not found in struct type %v", fieldName, c.structType))
	}
	c.fields = append(c.fields, fieldInfo{name: fieldName, typeTag: typeTag, typeInfo: field.Type})
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
	for _, field := range c.fields {
		fieldVal := val.FieldByName(field.name)
		if !fieldVal.IsValid() {
			return nil, fmt.Errorf("field %s not found in struct value", field.name)
		}

		// MODIFIED: Extract interface safely for interface{} types
		var fieldValue interface{} = fieldVal.Interface()
		if fieldVal.Kind() == reflect.Interface && !fieldVal.IsNil() {
			fieldValue = fieldVal.Elem().Interface()
		}

		codec, err := c.registry.GetCodec(field.typeTag)
		if err != nil {
			return nil, fmt.Errorf("error getting codec for field %s: %w", field.name, err)
		}
		encodedValue, err := codec.Encode(fieldValue)
		if err != nil {
			return nil, fmt.Errorf("error encoding field %s: %w", field.name, err)
		}
		buffer.WriteByte(field.typeTag)
		buffer.WriteByte(2)
		binary.Write(&buffer, binary.BigEndian, uint16(len(encodedValue)))
		buffer.Write(encodedValue)
	}
	return buffer.Bytes(), nil
}

func (c *StructCodec) Decode(data []byte) (interface{}, error) {
	result := reflect.New(c.structType).Elem()
	reader := bytes.NewReader(data)
	for _, field := range c.fields {
		var tag byte
		if err := binary.Read(reader, binary.BigEndian, &tag); err != nil {
			return nil, fmt.Errorf("failed to read field tag for %s: %w", field.name, err)
		}
		if tag != field.typeTag {
			return nil, fmt.Errorf("type mismatch for field %s: expected tag %d, got %d", field.name, field.typeTag, tag)
		}
		var lol byte
		if err := binary.Read(reader, binary.BigEndian, &lol); err != nil {
			return nil, fmt.Errorf("failed to read length-of-length for %s: %w", field.name, err)
		}
		var length uint16
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return nil, fmt.Errorf("failed to read length for %s: %w", field.name, err)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return nil, fmt.Errorf("failed to read payload for %s: %w", field.name, err)
		}
		codec, err := c.registry.GetCodec(tag)
		if err != nil {
			return nil, fmt.Errorf("error getting codec for field %s: %w", field.name, err)
		}
		decodedValue, err := codec.Decode(payload)
		if err != nil {
			return nil, fmt.Errorf("error decoding field %s: %w", field.name, err)
		}
		structField := result.FieldByName(field.name)
		if structField.IsValid() && structField.CanSet() {
			val := reflect.ValueOf(decodedValue)
			if val.Type().ConvertibleTo(structField.Type()) {
				structField.Set(val.Convert(structField.Type()))
			} else if structField.Type() == reflect.TypeOf((*interface{})(nil)).Elem() {
				structField.Set(val)
			} else {
				return nil, fmt.Errorf("cannot convert decoded value %v (%v) to field type %v for field %s", decodedValue, val.Type(), structField.Type(), field.name)
			}
		}
	}
	return result.Interface(), nil
}

// --- NEW: Support for map[string]interface{} and interface{} ---

// InterfaceCodec handles any type by storing its concrete type tag and data.
type InterfaceCodec struct {
	registry *CodecRegistry
}

func (c *InterfaceCodec) Encode(value interface{}) ([]byte, error) {
	if value == nil {
		// Represent nil as 0 length? Or specific tag?
		// Simplest is to return empty bytes or handle as specific type.
		// Here we treat nil as valid but empty payload for the interface itself,
		// but the TLV wrapper handles length.
		// However, we need to know what type it is to decode.
		// Standard convention: empty bytes = nil.
		return []byte{}, nil
	}

	tag, err := c.registry.GetTag(value)
	if err != nil {
		return nil, err
	}

	payload, err := c.registry.GetCodec(tag)
	if err != nil {
		return nil, err
	}

	data, err := payload.Encode(value)
	if err != nil {
		return nil, err
	}

	// Format: Tag(1) | Len(2) | Data
	buf := make([]byte, 1+2+len(data))
	buf[0] = tag
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(data)))
	copy(buf[3:], data)
	return buf, nil
}

func (c *InterfaceCodec) Decode(data []byte) (interface{}, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) < 3 {
		return nil, fmt.Errorf("invalid interface data: too short")
	}

	tag := data[0]
	length := binary.BigEndian.Uint16(data[1:3])
	if uint16(len(data)) < 3+length {
		return nil, fmt.Errorf("invalid interface data: length mismatch")
	}

	codec, err := c.registry.GetCodec(tag)
	if err != nil {
		return nil, err
	}

	return codec.Decode(data[3 : 3+length])
}

// MapStringAnyCodec handles map[string]interface{}.
type MapStringAnyCodec struct {
	registry *CodecRegistry
}

func (c *MapStringAnyCodec) Encode(value interface{}) ([]byte, error) {
	m, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("value is not map[string]interface{}")
	}

	// We use a helper buffer to calculate size
	buf := &bytes.Buffer{}

	// Count
	if err := binary.Write(buf, binary.BigEndian, uint32(len(m))); err != nil {
		return nil, err
	}

	stringCodec := &StringCodec{}
	anyCodec := &InterfaceCodec{registry: c.registry}

	for k, v := range m {
		// Key
		kBytes, err := stringCodec.Encode(k)
		if err != nil {
			return nil, err
		}
		if err := binary.Write(buf, binary.BigEndian, uint16(len(kBytes))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(kBytes); err != nil {
			return nil, err
		}

		// Value (using InterfaceCodec logic which outputs Tag+Len+Val)
		vBytes, err := anyCodec.Encode(v)
		if err != nil {
			return nil, fmt.Errorf("encoding map value for key %s: %w", k, err)
		}
		if err := binary.Write(buf, binary.BigEndian, uint16(len(vBytes))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(vBytes); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func (c *MapStringAnyCodec) Decode(data []byte) (interface{}, error) {
	reader := bytes.NewReader(data)

	var count uint32
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return nil, err
	}

	result := make(map[string]interface{}, count)
	stringCodec := &StringCodec{}
	anyCodec := &InterfaceCodec{registry: c.registry}

	for i := 0; i < int(count); i++ {
		// Key Length
		var kLen uint16
		if err := binary.Read(reader, binary.BigEndian, &kLen); err != nil {
			return nil, err
		}
		// Key Data
		kBytes := make([]byte, kLen)
		if _, err := io.ReadFull(reader, kBytes); err != nil {
			return nil, err
		}
		keyVal, err := stringCodec.Decode(kBytes)
		if err != nil {
			return nil, err
		}
		key := keyVal.(string)

		// Val Length
		var vLen uint16
		if err := binary.Read(reader, binary.BigEndian, &vLen); err != nil {
			return nil, err
		}
		// Val Data
		vBytes := make([]byte, vLen)
		if _, err := io.ReadFull(reader, vBytes); err != nil {
			return nil, err
		}
		val, err := anyCodec.Decode(vBytes)
		if err != nil {
			return nil, fmt.Errorf("decoding map value for key %s: %w", key, err)
		}

		result[key] = val
	}

	return result, nil
}

// --- NEW: Support for private/built-in structs via BinaryMarshaler ---

// MarshalerCodec wraps types that implement encoding.BinaryMarshaler/Unmarshaler.
// This is useful for time.Time and other stdlib types you cannot introspect.
type MarshalerCodec struct {
	typ reflect.Type
}

func (c *MarshalerCodec) Encode(value interface{}) ([]byte, error) {
	m, ok := value.(encoding.BinaryMarshaler)
	if !ok {
		return nil, fmt.Errorf("type %v does not implement BinaryMarshaler", c.typ)
	}
	data, err := m.MarshalBinary()
	if err != nil {
		return nil, err
	}
	// Prefix with length so we know how many bytes to consume during Unmarshal
	buf := make([]byte, 2+len(data))
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(data)))
	copy(buf[2:], data)
	return buf, nil
}

func (c *MarshalerCodec) Decode(data []byte) (interface{}, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("invalid data for MarshalerCodec: too short")
	}
	length := binary.BigEndian.Uint16(data[0:2])
	if uint16(len(data)) < 2+length {
		return nil, fmt.Errorf("invalid data for MarshalerCodec: length mismatch")
	}

	// Create a new instance of the concrete type
	ptr := reflect.New(c.typ)
	instance := ptr.Interface()

	u, ok := instance.(encoding.BinaryUnmarshaler)
	if !ok {
		return nil, fmt.Errorf("type %v does not implement BinaryUnmarshaler", c.typ)
	}

	if err := u.UnmarshalBinary(data[2 : 2+length]); err != nil {
		return nil, err
	}

	return ptr.Elem().Interface(), nil
}

// func main() {
// 	registry := NewCodecRegistry()
// 	registry.RegisterPrimitives()
// 	registry.RegisterStruct(Asdasd{})
// }
// type Asdasd struct {
// 	types map[reflect.Type]any
// 	time time.Time
// }
