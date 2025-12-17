// codec/primitives.go
package cryodecoder

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var ErrTypeMismatch = errors.New("type mismatch")

type Int32Codec struct{}

func (Int32Codec) Encode(v any) ([]byte, error) {
	i, ok := v.(int32)
	if !ok {
		return nil, ErrTypeMismatch
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(i))
	return b, nil
}

func (Int32Codec) Decode(b []byte) (any, error) {
	if len(b) != 4 {
		return nil, ErrTypeMismatch
	}
	return int32(binary.BigEndian.Uint32(b)), nil
}

type StringCodec struct{}

func (StringCodec) Encode(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, ErrTypeMismatch
	}
	return []byte(s), nil
}

func (StringCodec) Decode(b []byte) (any, error) {
	return string(b), nil
}

type Float64Codec struct{}

func (Float64Codec) Encode(v any) ([]byte, error) {
	f, ok := v.(float64)
	if !ok {
		return nil, ErrTypeMismatch
	}
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.BigEndian, f)
	return buf.Bytes(), err
}

func (Float64Codec) Decode(b []byte) (any, error) {
	var f float64
	err := binary.Read(bytes.NewReader(b), binary.BigEndian, &f)
	return f, err
}

