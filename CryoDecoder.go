// Package cryodecoder provides a high-performance, type-safe, extensible
// binary encoding/decoding system using a TLV (Tag-Length-Value) format.
package CryoDecoder

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"reflect"
	"time"
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
// MODIFIED: Added tags 20 (time.Location) and updated interface/map tags.
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
	r.RegisterCodec(18, &InterfaceCodec{registry: r}, []any{}) // interface{}
	r.RegisterCodec(19, &MapStringAnyCodec{registry: r}, map[string]interface{}(nil))

	// NEW: Register time.Location specifically
	r.RegisterCodec(20, &LocationCodec{}, time.Location{})
}

// resolveType finds or creates a codec for the given reflect.Type.
// MODIFIED: Now handles pointers, slices, arrays, and maps automatically by wrapping the underlying type's codec.
func (r *CodecRegistry) resolveType(t reflect.Type) (reflect.Type, byte, error) {
	// 1. Check direct registry
	if tag, exists := r.types[t]; exists {
		return t, tag, nil
	}

	// 2. Handle Pointers recursively
	if t.Kind() == reflect.Ptr {
		elemType, elemTag, err := r.resolveType(t.Elem())
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve pointer element %v: %w", t.Elem(), err)
		}

		// We found a tag for the element. We need to create a PointerCodec for the pointer type itself.
		// Get the codec for the element
		elemCodec, err := r.GetCodec(elemTag)
		if err != nil {
			return nil, 0, err
		}

		// Create a wrapper codec for the pointer
		ptrTag := r.nextStructTag
		r.nextStructTag++

		// Create a zero instance of the pointer to register the type
		ptrZero := reflect.New(t.Elem()).Interface()

		r.RegisterCodec(ptrTag, &PointerCodec{elemCodec: elemCodec, elemType: elemType}, ptrZero)
		return t, ptrTag, nil
	}

	// 3. Handle Slices recursively
	if t.Kind() == reflect.Slice {
		elemType, elemTag, err := r.resolveType(t.Elem())
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve slice element %v: %w", t.Elem(), err)
		}

		elemCodec, err := r.GetCodec(elemTag)
		if err != nil {
			return nil, 0, err
		}

		sliceTag := r.nextStructTag
		r.nextStructTag++

		// Create a zero instance of the slice to register the type
		sliceZero := reflect.MakeSlice(t, 0, 0).Interface()

		r.RegisterCodec(sliceTag, &SliceCodec{elemCodec: elemCodec, elemType: elemType}, sliceZero)
		return t, sliceTag, nil
	}

	// 4. Handle Arrays recursively
	if t.Kind() == reflect.Array {
		elemType, elemTag, err := r.resolveType(t.Elem())
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve array element %v: %w", t.Elem(), err)
		}

		elemCodec, err := r.GetCodec(elemTag)
		if err != nil {
			return nil, 0, err
		}

		arrayTag := r.nextStructTag
		r.nextStructTag++

		// Create a zero instance of the array to register the type
		arrayZero := reflect.New(t).Elem().Interface()

		r.RegisterCodec(arrayTag, &ArrayCodec{elemCodec: elemCodec, elemType: elemType, arrayLen: t.Len()}, arrayZero)
		return t, arrayTag, nil
	}

	// 5. Handle Maps recursively
	if t.Kind() == reflect.Map {
		keyType, keyTag, err := r.resolveType(t.Key())
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve map key type %v: %w", t.Key(), err)
		}

		valType, valTag, err := r.resolveType(t.Elem())
		if err != nil {
			return nil, 0, fmt.Errorf("failed to resolve map value type %v: %w", t.Elem(), err)
		}

		keyCodec, err := r.GetCodec(keyTag)
		if err != nil {
			return nil, 0, err
		}

		valCodec, err := r.GetCodec(valTag)
		if err != nil {
			return nil, 0, err
		}

		mapTag := r.nextStructTag
		r.nextStructTag++

		// Create a zero instance of the map to register the type
		mapZero := reflect.MakeMap(t).Interface()

		r.RegisterCodec(mapTag, &MapCodec{keyCodec: keyCodec, valCodec: valCodec, keyType: keyType, valType: valType}, mapZero)
		return t, mapTag, nil
	}

	// 6. Handle specific known types (e.g. time.Location) that we can't introspect
	if t.PkgPath() == "time" && t.Name() == "Location" {
		locTag := r.nextStructTag
		r.nextStructTag++
		r.RegisterCodec(locTag, &LocationCodec{}, reflect.New(t).Elem().Interface())
		return t, locTag, nil
	}

	// 7. Check for BinaryMarshaler (for other built-in types)
	if t.Implements(reflect.TypeOf((*encoding.BinaryMarshaler)(nil)).Elem()) {
		marshalTag := r.nextStructTag
		r.nextStructTag++
		zeroValue := reflect.New(t).Elem().Interface()
		r.RegisterCodec(marshalTag, &MarshalerCodec{typ: t}, zeroValue)
		return t, marshalTag, nil
	}

	// 8. Handle Structs (Recursion)
	if t.Kind() == reflect.Struct {
		zeroValue := reflect.New(t).Elem().Interface()
		structTag, err := r.RegisterStruct(zeroValue)
		if err != nil {
			return nil, 0, err
		}
		return t, structTag, nil
	}

	return nil, 0, fmt.Errorf("no codec found for type %v", t)
}

// RegisterStruct automatically registers a custom struct and all of its nested structs.
// MODIFIED: Uses resolveType to handle pointers, nested structs, and specific types automatically.
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

	codec := NewStructCodec(r, exampleType)

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		fieldType := field.Type

		// Use resolveType to handle the complexity of pointers, locations, collections, etc.
		_, typeTag, err := r.resolveType(fieldType)
		if err != nil {
			return 0, fmt.Errorf("failed to resolve codec for field '%s' (%v): %w", field.Name, fieldType, err)
		}

		codec.RegisterField(field.Name, typeTag)
	}

	structTag := r.nextStructTag
	r.nextStructTag++

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
	t := reflect.TypeOf(value)

	// First, check if the type is already registered
	if tag, exists := r.types[t]; exists {
		return tag, nil
	}

	// If not registered, try to resolve it (handles collections, pointers, etc.)
	_, tag, err := r.resolveType(t)
	if err != nil {
		return 0, fmt.Errorf("no tag registered for type %T: %w", value, err)
	}

	return tag, nil
}

// --- Encoder and Decoder ---

type Encoder struct {
	registry *CodecRegistry
	buffer   *bytes.Buffer
}

func NewEncoder(registry *CodecRegistry) *Encoder {
	return &Encoder{registry: registry, buffer: &bytes.Buffer{}}
}

func (e *Encoder) Encode(value interface{}) ([]byte, error) {
	e.buffer.Reset()
	if err := e.buffer.WriteByte(BOF); err != nil {
		return nil, fmt.Errorf("failed to write BOF marker: %w", err)
	}

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

type Decoder struct {
	registry *CodecRegistry
	reader   io.Reader
}

func NewDecoder(registry *CodecRegistry, reader io.Reader) *Decoder {
	return &Decoder{registry: registry, reader: reader}
}

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

type InterfaceCodec struct {
	registry *CodecRegistry
}

func (c *InterfaceCodec) Encode(value interface{}) ([]byte, error) {
	if value == nil {
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

type MapStringAnyCodec struct {
	registry *CodecRegistry
}

func (c *MapStringAnyCodec) Encode(value interface{}) ([]byte, error) {
	m, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("value is not map[string]interface{}")
	}

	buf := &bytes.Buffer{}

	if err := binary.Write(buf, binary.BigEndian, uint32(len(m))); err != nil {
		return nil, err
	}

	stringCodec := &StringCodec{}
	anyCodec := &InterfaceCodec{registry: c.registry}

	for k, v := range m {
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
		var kLen uint16
		if err := binary.Read(reader, binary.BigEndian, &kLen); err != nil {
			return nil, err
		}
		kBytes := make([]byte, kLen)
		if _, err := io.ReadFull(reader, kBytes); err != nil {
			return nil, err
		}
		keyVal, err := stringCodec.Decode(kBytes)
		if err != nil {
			return nil, err
		}
		key := keyVal.(string)

		var vLen uint16
		if err := binary.Read(reader, binary.BigEndian, &vLen); err != nil {
			return nil, err
		}
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

// --- NEW: Collection Codecs (Slices, Arrays, Maps) ---

// SliceCodec handles slice types []T.
// It stores the count of elements followed by each encoded element.
type SliceCodec struct {
	elemCodec Codec
	elemType  reflect.Type
}

func (c *SliceCodec) Encode(value interface{}) ([]byte, error) {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		return nil, fmt.Errorf("SliceCodec expects a slice, got %T", value)
	}

	buf := &bytes.Buffer{}

	// Write the count of elements
	count := uint32(rv.Len())
	if err := binary.Write(buf, binary.BigEndian, count); err != nil {
		return nil, err
	}

	// Encode each element
	for i := 0; i < rv.Len(); i++ {
		elemVal := rv.Index(i).Interface()
		elemData, err := c.elemCodec.Encode(elemVal)
		if err != nil {
			return nil, fmt.Errorf("error encoding slice element %d: %w", i, err)
		}

		// Write element length and data
		if err := binary.Write(buf, binary.BigEndian, uint32(len(elemData))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(elemData); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func (c *SliceCodec) Decode(data []byte) (interface{}, error) {
	reader := bytes.NewReader(data)

	var count uint32
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return nil, fmt.Errorf("failed to read slice count: %w", err)
	}

	// Create a slice of the appropriate type
	sliceType := reflect.SliceOf(c.elemType)
	slice := reflect.MakeSlice(sliceType, int(count), int(count))

	for i := 0; i < int(count); i++ {
		var elemLen uint32
		if err := binary.Read(reader, binary.BigEndian, &elemLen); err != nil {
			return nil, fmt.Errorf("failed to read element %d length: %w", i, err)
		}

		elemData := make([]byte, elemLen)
		if _, err := io.ReadFull(reader, elemData); err != nil {
			return nil, fmt.Errorf("failed to read element %d data: %w", i, err)
		}

		elemVal, err := c.elemCodec.Decode(elemData)
		if err != nil {
			return nil, fmt.Errorf("failed to decode element %d: %w", i, err)
		}

		// Set the element in the slice
		rv := reflect.ValueOf(elemVal)
		if rv.Type().ConvertibleTo(c.elemType) {
			rv = rv.Convert(c.elemType)
		}
		slice.Index(i).Set(rv)
	}

	return slice.Interface(), nil
}

// ArrayCodec handles array types [N]T.
// It stores each encoded element in order.
type ArrayCodec struct {
	elemCodec Codec
	elemType  reflect.Type
	arrayLen  int
}

func (c *ArrayCodec) Encode(value interface{}) ([]byte, error) {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("ArrayCodec expects an array, got %T", value)
	}

	if rv.Len() != c.arrayLen {
		return nil, fmt.Errorf("array length mismatch: expected %d, got %d", c.arrayLen, rv.Len())
	}

	buf := &bytes.Buffer{}

	// Encode each element
	for i := 0; i < rv.Len(); i++ {
		elemVal := rv.Index(i).Interface()
		elemData, err := c.elemCodec.Encode(elemVal)
		if err != nil {
			return nil, fmt.Errorf("error encoding array element %d: %w", i, err)
		}

		// Write element length and data
		if err := binary.Write(buf, binary.BigEndian, uint32(len(elemData))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(elemData); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func (c *ArrayCodec) Decode(data []byte) (interface{}, error) {
	reader := bytes.NewReader(data)

	// Create an array of the appropriate type
	arrayType := reflect.ArrayOf(c.arrayLen, c.elemType)
	array := reflect.New(arrayType).Elem()

	for i := 0; i < c.arrayLen; i++ {
		var elemLen uint32
		if err := binary.Read(reader, binary.BigEndian, &elemLen); err != nil {
			return nil, fmt.Errorf("failed to read array element %d length: %w", i, err)
		}

		elemData := make([]byte, elemLen)
		if _, err := io.ReadFull(reader, elemData); err != nil {
			return nil, fmt.Errorf("failed to read array element %d data: %w", i, err)
		}

		elemVal, err := c.elemCodec.Decode(elemData)
		if err != nil {
			return nil, fmt.Errorf("failed to decode array element %d: %w", i, err)
		}

		// Set the element in the array
		rv := reflect.ValueOf(elemVal)
		if rv.Type().ConvertibleTo(c.elemType) {
			rv = rv.Convert(c.elemType)
		}
		array.Index(i).Set(rv)
	}

	return array.Interface(), nil
}

// MapCodec handles map types map[K]V.
// It stores the count of entries followed by each key-value pair.
type MapCodec struct {
	keyCodec Codec
	valCodec Codec
	keyType  reflect.Type
	valType  reflect.Type
}

func (c *MapCodec) Encode(value interface{}) ([]byte, error) {
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Map {
		return nil, fmt.Errorf("MapCodec expects a map, got %T", value)
	}

	buf := &bytes.Buffer{}

	// Write the count of entries
	count := uint32(rv.Len())
	if err := binary.Write(buf, binary.BigEndian, count); err != nil {
		return nil, err
	}

	// Encode each key-value pair
	for _, keyVal := range rv.MapKeys() {
		key := keyVal.Interface()
		val := rv.MapIndex(keyVal).Interface()

		keyData, err := c.keyCodec.Encode(key)
		if err != nil {
			return nil, fmt.Errorf("error encoding map key %v: %w", key, err)
		}

		valData, err := c.valCodec.Encode(val)
		if err != nil {
			return nil, fmt.Errorf("error encoding map value for key %v: %w", key, err)
		}

		// Write key length and data
		if err := binary.Write(buf, binary.BigEndian, uint32(len(keyData))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(keyData); err != nil {
			return nil, err
		}

		// Write value length and data
		if err := binary.Write(buf, binary.BigEndian, uint32(len(valData))); err != nil {
			return nil, err
		}
		if _, err := buf.Write(valData); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

func (c *MapCodec) Decode(data []byte) (interface{}, error) {
	reader := bytes.NewReader(data)

	var count uint32
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return nil, fmt.Errorf("failed to read map count: %w", err)
	}

	// Create a map of the appropriate type
	mapType := reflect.MapOf(c.keyType, c.valType)
	m := reflect.MakeMap(mapType)

	for i := 0; i < int(count); i++ {
		var keyLen uint32
		if err := binary.Read(reader, binary.BigEndian, &keyLen); err != nil {
			return nil, fmt.Errorf("failed to read map entry %d key length: %w", i, err)
		}

		keyData := make([]byte, keyLen)
		if _, err := io.ReadFull(reader, keyData); err != nil {
			return nil, fmt.Errorf("failed to read map entry %d key data: %w", i, err)
		}

		keyVal, err := c.keyCodec.Decode(keyData)
		if err != nil {
			return nil, fmt.Errorf("failed to decode map entry %d key: %w", i, err)
		}

		var valLen uint32
		if err := binary.Read(reader, binary.BigEndian, &valLen); err != nil {
			return nil, fmt.Errorf("failed to read map entry %d value length: %w", i, err)
		}

		valData := make([]byte, valLen)
		if _, err := io.ReadFull(reader, valData); err != nil {
			return nil, fmt.Errorf("failed to read map entry %d value data: %w", i, err)
		}

		valVal, err := c.valCodec.Decode(valData)
		if err != nil {
			return nil, fmt.Errorf("failed to decode map entry %d value: %w", i, err)
		}

		// Set the key-value pair in the map
		keyRv := reflect.ValueOf(keyVal)
		if keyRv.Type().ConvertibleTo(c.keyType) {
			keyRv = keyRv.Convert(c.keyType)
		}

		valRv := reflect.ValueOf(valVal)
		if valRv.Type().ConvertibleTo(c.valType) {
			valRv = valRv.Convert(c.valType)
		}

		m.SetMapIndex(keyRv, valRv)
	}

	return m.Interface(), nil
}

// --- NEW: Specialized Codecs ---

// LocationCodec handles time.Location.
// It serializes the location name (e.g., "UTC", "America/New_York").
// Note: This is best-effort. Custom locations created via FixedZone might not be portable.
type LocationCodec struct{}

func (c *LocationCodec) Encode(value interface{}) ([]byte, error) {
	loc, ok := value.(time.Location)
	if !ok {
		return nil, fmt.Errorf("value is not time.Location")
	}
	return []byte(loc.String()), nil
}

func (c *LocationCodec) Decode(data []byte) (interface{}, error) {
	name := string(data)
	if name == "" {
		return time.UTC, nil // Default to UTC if empty, or handle as error
	}

	// time.LoadLocation requires I/O and might be slow.
	// For standard names it's usually cached.
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("failed to load location %s: %w", name, err)
	}
	return *loc, nil
}

// PointerCodec handles pointer types (*T).
// It wraps the codec for T and adds logic to handle nil pointers.
type PointerCodec struct {
	elemCodec Codec
	elemType  reflect.Type
}

func (c *PointerCodec) Encode(value interface{}) ([]byte, error) {
	if value == nil {
		// 0x00 represents nil
		return []byte{0}, nil
	}

	// Dereference the pointer to get the value
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Ptr {
		return nil, fmt.Errorf("PointerCodec expects a pointer, got %T", value)
	}

	elemValue := rv.Elem().Interface()
	data, err := c.elemCodec.Encode(elemValue)
	if err != nil {
		return nil, err
	}

	// 0x01 represents valid pointer, followed by data
	result := make([]byte, 1+len(data))
	result[0] = 1
	copy(result[1:], data)
	return result, nil
}

func (c *PointerCodec) Decode(data []byte) (interface{}, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("invalid pointer data: empty")
	}

	if data[0] == 0 {
		// Return a nil pointer of the correct type
		return reflect.Zero(reflect.PtrTo(c.elemType)).Interface(), nil
	}

	// Decode the inner element
	elemVal, err := c.elemCodec.Decode(data[1:])
	if err != nil {
		return nil, err
	}

	// Wrap the element in a pointer
	rv := reflect.ValueOf(elemVal)
	if rv.Type().ConvertibleTo(c.elemType) {
		rv = rv.Convert(c.elemType)
	}

	ptr := reflect.New(c.elemType)
	ptr.Elem().Set(rv)
	return ptr.Interface(), nil
}

// --- Support for private/built-in structs via BinaryMarshaler ---

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
