// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

func int32Val(v any) int32 {
	switch x := v.(type) {
	case int32:
		return x
	case int:
		return int32(x)
	case int64:
		return int32(x)
	}
	return 0
}

func int64Val(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	}
	return 0
}

// Elem is one BSON element preserving protocol field order.
type Elem struct {
	Key  string
	Type byte
	Val  any // string, int32, int64, bool, float64, []Elem (doc/array), []byte, nil
}

const (
	tDouble = 0x01
	tString = 0x02
	tDoc    = 0x03
	tArr    = 0x04
	tBin    = 0x05
	tBool   = 0x08
	tArr9   = 0x09
	tNull   = 0x0a
	tInt32  = 0x10
	tInt64  = 0x12
)

func EncodeDoc(elems []Elem) []byte {
	var body []byte
	for _, e := range elems {
		body = append(body, e.Type)
		body = append(body, e.Key...)
		body = append(body, 0)
		switch e.Type {
		case tString:
			s := []byte(e.Val.(string))
			var l [4]byte
			binary.LittleEndian.PutUint32(l[:], uint32(len(s)+1))
			body = append(body, l[:]...)
			body = append(body, s...)
			body = append(body, 0)
		case tInt32:
			var l [4]byte
			binary.LittleEndian.PutUint32(l[:], uint32(int32Val(e.Val)))
			body = append(body, l[:]...)
		case tInt64:
			var l [8]byte
			binary.LittleEndian.PutUint64(l[:], uint64(int64Val(e.Val)))
			body = append(body, l[:]...)
		case tBool:
			b := byte(0)
			if e.Val.(bool) {
				b = 1
			}
			body = append(body, b)
		case tDouble:
			var l [8]byte
			binary.LittleEndian.PutUint64(l[:], math.Float64bits(e.Val.(float64)))
			body = append(body, l[:]...)
		case tDoc, tArr, tArr9:
			nested := EncodeDoc(e.Val.([]Elem))
			body = append(body, nested...)
		case tBin:
			b := e.Val.([]byte)
			var l [4]byte
			binary.LittleEndian.PutUint32(l[:], uint32(len(b)))
			body = append(body, l[:]...)
			body = append(body, 0) // subtype generic
			body = append(body, b...)
		case tNull:
			// nothing
		default:
			panic(fmt.Sprintf("bson: unhandled type 0x%02x", e.Type))
		}
	}
	body = append(body, 0)
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out, uint32(len(body)+4))
	copy(out[4:], body)
	return out
}

// stringVal / boolVal coerce a decoded BSON value to the Go type the caller wants; a missing or
// differently-typed field decodes to the zero value, which is what a bridge wants for optional
// fields (an absent flag means "no change", not "error").
func stringVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolVal(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func bitvalOf(v any) *uint64 { return nil }

// Array builds []Elem with numeric string keys for tArr9.
func Array(vals ...any) []Elem {
	out := make([]Elem, 0, len(vals))
	for i, v := range vals {
		var e Elem
		switch v.(type) {
		case int64:
			e = Elem{Key: fmt.Sprint(i), Type: tInt64, Val: v}
		case int:
			e = Elem{Key: fmt.Sprint(i), Type: tInt64, Val: int64(v.(int))}
		case int32:
			e = Elem{Key: fmt.Sprint(i), Type: tInt32, Val: v}
		case string:
			e = Elem{Key: fmt.Sprint(i), Type: tString, Val: v}
		}
		out = append(out, e)
	}
	return out
}

var ErrShort = errors.New("bson: short buffer")

func DecodeDoc(b []byte, off int) ([]Elem, int, error) {
	if off+4 > len(b) {
		return nil, 0, ErrShort
	}
	n := int(binary.LittleEndian.Uint32(b[off:]))
	if n < 5 || off+n > len(b) {
		return nil, 0, ErrShort
	}
	end := off + n
	i := off + 4
	var out []Elem
	for i < end-1 {
		t := b[i]
		i++
		j := i
		for j < end && b[j] != 0 {
			j++
		}
		if j >= end {
			return out, i, ErrShort
		}
		key := string(b[i:j])
		i = j + 1
		var v any
		switch t {
		case tString:
			if i+4 > end {
				return out, i, ErrShort
			}
			l := int(binary.LittleEndian.Uint32(b[i:]))
			i += 4
			if i+l > end {
				return out, i, ErrShort
			}
			v = string(b[i : i+l-1])
			i += l
		case tInt32:
			if i+4 > end {
				return out, i, ErrShort
			}
			v = int32(binary.LittleEndian.Uint32(b[i:]))
			i += 4
		case tInt64:
			if i+8 > end {
				return out, i, ErrShort
			}
			v = int64(binary.LittleEndian.Uint64(b[i:]))
			i += 8
		case tBool:
			v = b[i] != 0
			i++
		case tDouble:
			v = math.Float64frombits(binary.LittleEndian.Uint64(b[i:]))
			i += 8
		case tDoc, tArr, tArr9:
			nested, ni, err := DecodeDoc(b, i)
			if err != nil {
				return out, ni, err
			}
			v = nested
			i = ni
		case tBin:
			l := int(binary.LittleEndian.Uint32(b[i:]))
			i += 5
			v = append([]byte(nil), b[i:i+l]...)
			i += l
		case tNull:
			v = nil
		default:
			return out, i, fmt.Errorf("bson: type 0x%02x key %q at %d", t, key, i)
		}
		out = append(out, Elem{Key: key, Type: t, Val: v})
	}
	return out, end, nil
}

func Get(elems []Elem, key string) any {
	for _, e := range elems {
		if e.Key == key {
			return e.Val
		}
	}
	return nil
}

func InnerStatus(body []byte) int64 {
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return -99999
	}
	switch v := Get(elems, "status").(type) {
	case int32:
		return int64(v)
	case int64:
		return v
	}
	return 0
}
