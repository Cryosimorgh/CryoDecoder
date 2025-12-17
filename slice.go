// codec/slice.go
package cryodecoder

import (
	"bytes"
	"encoding/binary"
)

type SliceCodec struct {
	Elem Codec
}

func (c SliceCodec) Encode(v any) ([]byte, error) {
	slice, ok := v.([]any)
	if !ok {
		return nil, ErrTypeMismatch
	}

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(len(slice)))

	for _, elem := range slice {
		data, err := c.Elem.Encode(elem)
		if err != nil {
			return nil, err
		}
		binary.Write(buf, binary.BigEndian, uint32(len(data)))
		buf.Write(data)
	}

	return buf.Bytes(), nil
}

func (c SliceCodec) Decode(b []byte) (any, error) {
	buf := bytes.NewReader(b)

	var count uint32
	binary.Read(buf, binary.BigEndian, &count)

	out := make([]any, 0, count)

	for i := uint32(0); i < count; i++ {
		var l uint32
		binary.Read(buf, binary.BigEndian, &l)

		data := make([]byte, l)
		buf.Read(data)

		v, err := c.Elem.Decode(data)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}

	return out, nil
}

