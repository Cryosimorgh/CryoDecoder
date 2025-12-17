// codec/struct.go
package cryodecoder

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

type StructField struct {
	Tag   uint8
	Codec Codec
	Name  string
}

type StructCodec struct {
	Fields []StructField
}

func (c StructCodec) Encode(v any) ([]byte, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ErrTypeMismatch
	}

	buf := new(bytes.Buffer)

	for _, f := range c.Fields {
		val, ok := obj[f.Name]
		if !ok {
			continue
		}

		data, err := f.Codec.Encode(val)
		if err != nil {
			return nil, err
		}

		buf.WriteByte(f.Tag)
		buf.WriteByte(2)
		binary.Write(buf, binary.BigEndian, uint16(len(data)))
		buf.Write(data)
	}

	return buf.Bytes(), nil
}

func (c StructCodec) Decode(b []byte) (any, error) {
	out := make(map[string]any)
	buf := bytes.NewReader(b)

	for buf.Len() > 0 {
		tag, _ := buf.ReadByte()
		buf.ReadByte() // len-of-len

		var l uint16
		binary.Read(buf, binary.BigEndian, &l)

		data := make([]byte, l)
		buf.Read(data)

		var field *StructField
		for i := range c.Fields {
			if c.Fields[i].Tag == tag {
				field = &c.Fields[i]
				break
			}
		}
		if field == nil {
			return nil, fmt.Errorf("unknown struct tag %d", tag)
		}

		val, err := field.Codec.Decode(data)
		if err != nil {
			return nil, err
		}

		out[field.Name] = val
	}

	return out, nil
}

