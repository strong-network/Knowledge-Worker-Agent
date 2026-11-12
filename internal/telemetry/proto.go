// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"encoding/binary"
	"math"
)

// buf writes Protocol Buffers by hand: the three OTLP export requests use a
// small part of the schema, and the generated Go code would bring the protobuf
// and OpenTelemetry modules into a module with three direct dependencies.
type buf []byte

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
)

func (b *buf) tag(field, wire int) { b.varint(uint64(field)<<3 | uint64(wire)) }

func (b *buf) varint(v uint64) { *b = binary.AppendUvarint(*b, v) }

func (b *buf) uint(field int, v uint64) {
	if v == 0 {
		return
	}
	b.tag(field, wireVarint)
	b.varint(v)
}

func (b *buf) boolean(field int, v bool) {
	if v {
		b.uint(field, 1)
	}
}

func (b *buf) fixed64(field int, v uint64) {
	if v == 0 {
		return
	}
	b.tag(field, wireFixed64)
	*b = binary.LittleEndian.AppendUint64(*b, v)
}

func (b *buf) double(field int, v float64) {
	b.tag(field, wireFixed64)
	*b = binary.LittleEndian.AppendUint64(*b, math.Float64bits(v))
}

func (b *buf) bytes(field int, v []byte) {
	if len(v) == 0 {
		return
	}
	b.tag(field, wireBytes)
	b.varint(uint64(len(v)))
	*b = append(*b, v...)
}

func (b *buf) str(field int, v string) {
	if v == "" {
		return
	}
	b.tag(field, wireBytes)
	b.varint(uint64(len(v)))
	*b = append(*b, v...)
}

// msg writes a nested message built by fn.
func (b *buf) msg(field int, fn func(*buf)) {
	var inner buf
	fn(&inner)
	b.tag(field, wireBytes)
	b.varint(uint64(len(inner)))
	*b = append(*b, inner...)
}

// OTLP field numbers (opentelemetry-proto v1).
const (
	// common.v1
	anyString = 1
	anyBool   = 2
	anyInt    = 3
	anyDouble = 4
	kvKey     = 1
	kvValue   = 2
	scopeName = 1
	scopeVer  = 2
	// resource.v1
	resourceAttrs = 1

	// metrics.v1
	reqResourceMetrics = 1
	rmResource         = 1
	rmScopeMetrics     = 2
	smScope            = 1
	smMetrics          = 2
	metricName         = 1
	metricUnit         = 3
	metricSum          = 7
	sumDataPoints      = 1
	sumTemporality     = 2
	sumMonotonic       = 3
	dpStartTime        = 2
	dpTime             = 3
	dpAsDouble         = 4
	dpAttributes       = 7
	temporalityDelta   = 1
	reqResourceLogs    = 1
	rlResource         = 1
	rlScopeLogs        = 2
	slScope            = 1
	slLogRecords       = 2
	logTime            = 1
	logBody            = 5
	logAttributes      = 6
	logTraceID         = 9
	logSpanID          = 10
	logObservedTime    = 11
	logEventName       = 12
	reqResourceSpans   = 1
	rsResource         = 1
	rsScopeSpans       = 2
	ssScope            = 1
	ssSpans            = 2
	spanTraceID        = 1
	spanSpanID         = 2
	spanParentID       = 4
	spanName           = 5
	spanKind           = 6
	spanStart          = 7
	spanEnd            = 8
	spanAttributes     = 9
	spanEvents         = 11
	spanStatus         = 15
	eventTime          = 1
	eventName          = 2
	eventAttributes    = 3
	statusCode         = 3
	spanKindInternal   = 1
	statusCodeError    = 2
)

func (b *buf) anyValue(field int, v any) {
	b.msg(field, func(m *buf) {
		switch x := v.(type) {
		case string:
			// An empty string is still a string value; write the field anyway.
			m.tag(anyString, wireBytes)
			m.varint(uint64(len(x)))
			*m = append(*m, x...)
		case bool:
			m.tag(anyBool, wireVarint)
			if x {
				m.varint(1)
			} else {
				m.varint(0)
			}
		case int:
			m.tag(anyInt, wireVarint)
			m.varint(uint64(int64(x)))
		case int64:
			m.tag(anyInt, wireVarint)
			m.varint(uint64(x))
		case float64:
			m.double(anyDouble, x)
		}
	})
}

func (b *buf) attrs(field int, attrs []Attr) {
	for _, a := range attrs {
		b.msg(field, func(m *buf) {
			m.str(kvKey, a.Key)
			m.anyValue(kvValue, a.Value)
		})
	}
}
