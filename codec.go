// codec/codec.go
package cryodecoder

type Codec interface {
	Encode(any) ([]byte, error)
	Decode([]byte) (any, error)
}

