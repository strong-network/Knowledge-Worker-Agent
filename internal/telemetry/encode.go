// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "time"

func nanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

func (e *Exporter) resourceMsg(b *buf) { b.attrs(resourceAttrs, e.resource) }

func scope(name string) func(*buf) {
	return func(b *buf) {
		b.str(scopeName, name)
		b.str(scopeVer, Version)
	}
}

// encodeMetrics writes an ExportMetricsServiceRequest of delta sums.
func (e *Exporter) encodeMetrics(points []*metricPoint, since, now time.Time) []byte {
	var b buf
	b.msg(reqResourceMetrics, func(rm *buf) {
		rm.msg(rmResource, e.resourceMsg)
		rm.msg(rmScopeMetrics, func(sm *buf) {
			sm.msg(smScope, scope(scopeMetrics))
			for i := 0; i < len(points); {
				// One metric per name, with a data point per attribute set.
				j := i
				for j < len(points) && points[j].name == points[i].name {
					j++
				}
				group := points[i:j]
				sm.msg(smMetrics, func(m *buf) {
					m.str(metricName, group[0].name)
					m.str(metricUnit, group[0].unit)
					m.msg(metricSum, func(s *buf) {
						for _, p := range group {
							s.msg(sumDataPoints, func(dp *buf) {
								dp.fixed64(dpStartTime, nanos(since))
								dp.fixed64(dpTime, nanos(now))
								dp.double(dpAsDouble, p.value)
								dp.attrs(dpAttributes, p.attrs)
							})
						}
						s.uint(sumTemporality, temporalityDelta)
						s.boolean(sumMonotonic, true)
					})
				})
				i = j
			}
		})
	})
	return b
}

// encodeLogs writes an ExportLogsServiceRequest of events.
func (e *Exporter) encodeLogs(records []logRecord) []byte {
	var b buf
	b.msg(reqResourceLogs, func(rl *buf) {
		rl.msg(rlResource, e.resourceMsg)
		rl.msg(rlScopeLogs, func(sl *buf) {
			sl.msg(slScope, scope(scopeEvents))
			for _, r := range records {
				sl.msg(slLogRecords, func(lr *buf) {
					lr.fixed64(logTime, nanos(r.time))
					lr.anyValue(logBody, "claude_code."+r.name)
					lr.attrs(logAttributes, r.attrs)
					lr.bytes(logTraceID, r.traceID)
					lr.bytes(logSpanID, r.spanID)
					lr.fixed64(logObservedTime, nanos(r.time))
					lr.str(logEventName, "claude_code."+r.name)
				})
			}
		})
	})
	return b
}

// encodeSpans writes an ExportTraceServiceRequest.
func (e *Exporter) encodeSpans(spans []span) []byte {
	var b buf
	b.msg(reqResourceSpans, func(rs *buf) {
		rs.msg(rsResource, e.resourceMsg)
		rs.msg(rsScopeSpans, func(ss *buf) {
			ss.msg(ssScope, scope(scopeTracing))
			for _, s := range spans {
				ss.msg(ssSpans, func(sp *buf) {
					sp.bytes(spanTraceID, s.traceID)
					sp.bytes(spanSpanID, s.spanID)
					sp.bytes(spanParentID, s.parentID)
					sp.str(spanName, s.name)
					sp.uint(spanKind, spanKindInternal)
					sp.fixed64(spanStart, nanos(s.start))
					sp.fixed64(spanEnd, nanos(s.end))
					sp.attrs(spanAttributes, s.attrs)
					for _, ev := range s.events {
						sp.msg(spanEvents, func(se *buf) {
							se.fixed64(eventTime, nanos(ev.time))
							se.str(eventName, ev.name)
							se.attrs(eventAttributes, ev.attrs)
						})
					}
					if s.failed {
						sp.msg(spanStatus, func(st *buf) { st.uint(statusCode, statusCodeError) })
					}
				})
			}
		})
	})
	return b
}
