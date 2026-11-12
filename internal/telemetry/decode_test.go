// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"encoding/binary"
	"fmt"
	"math"
)

// field is one decoded protobuf field. The tests decode what the encoder wrote
// with this separate, minimal reader, field number by field number.
type field struct {
	num   int
	wire  int
	u     uint64
	bytes []byte
}

func decode(b []byte) ([]field, error) {
	var out []field
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, fmt.Errorf("bad key")
		}
		b = b[n:]
		f := field{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case wireVarint:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, fmt.Errorf("bad varint")
			}
			f.u, b = v, b[n:]
		case wireFixed64:
			if len(b) < 8 {
				return nil, fmt.Errorf("short fixed64")
			}
			f.u, b = binary.LittleEndian.Uint64(b), b[8:]
		case wireBytes:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return nil, fmt.Errorf("bad length")
			}
			f.bytes, b = b[n:n+int(l)], b[n+int(l):]
		default:
			return nil, fmt.Errorf("wire type %d", f.wire)
		}
		out = append(out, f)
	}
	return out, nil
}

// msgs returns the decoded sub-messages of field num.
func msgs(b []byte, num int) [][]field {
	fs, err := decode(b)
	if err != nil {
		panic(err)
	}
	var out [][]field
	for _, f := range fs {
		if f.num == num && f.wire == wireBytes {
			sub, err := decode(f.bytes)
			if err != nil {
				panic(err)
			}
			out = append(out, sub)
		}
	}
	return out
}

func raw(fs []field, num int) []byte {
	for _, f := range fs {
		if f.num == num {
			return f.bytes
		}
	}
	return nil
}

func str(fs []field, num int) string { return string(raw(fs, num)) }

func num(fs []field, n int) uint64 {
	for _, f := range fs {
		if f.num == n {
			return f.u
		}
	}
	return 0
}

func dbl(fs []field, n int) float64 { return math.Float64frombits(num(fs, n)) }

// attrMap reads the KeyValue list in field n of a message.
func attrMap(msg []byte, n int) map[string]any {
	out := map[string]any{}
	for _, kv := range msgs(msg, n) {
		val := raw(kv, kvValue)
		fs, _ := decode(val)
		var v any
		for _, f := range fs {
			switch f.num {
			case anyString:
				v = string(f.bytes)
			case anyBool:
				v = f.u == 1
			case anyInt:
				v = int64(f.u)
			case anyDouble:
				v = math.Float64frombits(f.u)
			}
		}
		out[str(kv, kvKey)] = v
	}
	return out
}

func encodeOf(fs []field) []byte {
	var b buf
	for _, f := range fs {
		switch f.wire {
		case wireVarint:
			b.tag(f.num, wireVarint)
			b.varint(f.u)
		case wireFixed64:
			b.tag(f.num, wireFixed64)
			b = binary.LittleEndian.AppendUint64(b, f.u)
		case wireBytes:
			b.tag(f.num, wireBytes)
			b.varint(uint64(len(f.bytes)))
			b = append(b, f.bytes...)
		}
	}
	return b
}
