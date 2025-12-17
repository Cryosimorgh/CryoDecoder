// Package cryodecoder provides a high-performance, type-safe, extensible
// binary encoding/decoding system using a TLV (Tag-Length-Value) format.
//
// The system is designed for simplicity. After registering primitive types,
// complex structs can be registered with a single function call.
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
		types:         make(map[reflect.Type]byte),
		nextStructTag: 200, // Reserve tags 0-199 for primitives, start struct tags at 200
	}
}

// RegisterPrimitives is a convenience method to register the built-in primitive codecs.
// It assigns standard tags:
// int32(1), string(2), float64(3), int64(4), bool(5), int(6), int8(7), int16(8),
// uint(9), uint8(10), uint16(11), uint32(12), uint64(13), uintptr(14), float32(15),
// complex64(16), complex128(17).
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
}

// RegisterStruct automatically registers a custom struct and all of its nested structs.
// It uses reflection to discover fields and their types, creating and registering
// the necessary codecs. It returns the tag assigned to the registered struct.
func (r *CodecRegistry) RegisterStruct(exampleType interface{}) (byte, error) {
	structType := reflect.TypeOf(exampleType)
	if structType.Kind() == reflect.Ptr {
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

		// If the field is a nested struct, register it recursively first.
		if fieldType.Kind() == reflect.Struct {
			nestedInstance := reflect.New(fieldType).Interface()
			_, err := r.RegisterStruct(nestedInstance)
			if err != nil {
				return 0, fmt.Errorf("failed to register nested struct %s: %w", fieldType.Name(), err)
			}
		}

		// Find the tag for the field's type.
		fieldValue := reflect.New(fieldType).Elem().Interface()
		typeTag, err := r.GetTag(fieldValue)
		if err != nil {
			return 0, fmt.Errorf("no codec registered for field '%s' of type %v. Please ensure its type is registered first.", field.Name, fieldType)
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
		codec, err := c.registry.GetCodec(field.typeTag)
		if err != nil {
			return nil, fmt.Errorf("error getting codec for field %s: %w", field.name, err)
		}
		fieldValue := fieldVal.Interface()
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
			} else {
				return nil, fmt.Errorf("cannot convert decoded value %v (%v) to field type %v for field %s", decodedValue, val.Type(), structField.Type(), field.name)
			}
		}
	}
	return result.Interface(), nil
}
