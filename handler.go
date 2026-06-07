// Copyright 2026 tyranitar-mega
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package log4go

import (
	"context"
	"log/slog"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/log"
)

type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers ...slog.Handler) *multiHandler {
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return newMultiHandler(handlers...)
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return newMultiHandler(handlers...)
}

type otelHandler struct {
	logger   log.Logger
	attrs    []log.KeyValue
	group    string
	minLevel slog.Level
}

func newOtelHandler(logger log.Logger, minLevel slog.Level) *otelHandler {
	return &otelHandler{logger: logger, minLevel: minLevel}
}

func slogLevelToOTelSeverity(level slog.Level) log.Severity {
	s := int(level) + 12
	if s < 1 {
		return log.SeverityTrace1
	}
	if s > 24 {
		return log.SeverityFatal4
	}
	return log.Severity(s)
}

func slogValueToOTelValue(v slog.Value) log.Value {
	switch v.Kind() {
	case slog.KindBool:
		return log.BoolValue(v.Bool())
	case slog.KindDuration:
		return log.Int64Value(v.Duration().Nanoseconds())
	case slog.KindFloat64:
		return log.Float64Value(v.Float64())
	case slog.KindInt64:
		return log.Int64Value(v.Int64())
	case slog.KindString:
		return log.StringValue(v.String())
	case slog.KindTime:
		return log.Int64Value(v.Time().UnixNano())
	case slog.KindUint64:
		return log.Int64Value(int64(v.Uint64()))
	case slog.KindGroup:
		attrs := v.Group()
		kvs := make([]log.KeyValue, len(attrs))
		for i, attr := range attrs {
			kvs[i] = slogAttrToOTelKeyValue(attr)
		}
		return log.MapValue(kvs...)
	case slog.KindAny:
		val := v.Any()
		switch val := val.(type) {
		case error:
			return log.StringValue(val.Error())
		case string:
			return log.StringValue(val)
		case int:
			return log.Int64Value(int64(val))
		case int64:
			return log.Int64Value(val)
		case float64:
			return log.Float64Value(val)
		case bool:
			return log.BoolValue(val)
		case time.Time:
			return log.Int64Value(val.UnixNano())
		case time.Duration:
			return log.Int64Value(val.Nanoseconds())
		case []byte:
			return log.BytesValue(val)
		default:
			return log.StringValue(v.String())
		}
	default:
		return log.StringValue(v.String())
	}
}

func slogAttrToOTelKeyValue(attr slog.Attr) log.KeyValue {
	return log.KeyValue{
		Key:   attr.Key,
		Value: slogValueToOTelValue(attr.Value),
	}
}

func (h *otelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level < h.minLevel {
		return false
	}
	return h.logger.Enabled(ctx, log.EnabledParameters{
		Severity: slogLevelToOTelSeverity(level),
	})
}

func (h *otelHandler) Handle(ctx context.Context, record slog.Record) error {
	otelRecord := log.Record{}
	otelRecord.SetTimestamp(record.Time)
	otelRecord.SetObservedTimestamp(time.Now())
	otelRecord.SetSeverity(slogLevelToOTelSeverity(record.Level))
	otelRecord.SetSeverityText(record.Level.String())
	otelRecord.SetBody(log.StringValue(record.Message))

	for _, attr := range h.attrs {
		otelRecord.AddAttributes(attr)
	}

	record.Attrs(func(attr slog.Attr) bool {
		key := attr.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		otelRecord.AddAttributes(log.KeyValue{Key: key, Value: slogValueToOTelValue(attr.Value)})
		return true
	})

	if record.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
		otelRecord.AddAttributes(
			log.String("code.filepath", frame.File),
			log.Int("code.lineno", frame.Line),
			log.String("code.function", frame.Function),
		)
	}

	h.logger.Emit(ctx, otelRecord)
	return nil
}

func (h *otelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]log.KeyValue, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	for i, attr := range attrs {
		key := attr.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		newAttrs[len(h.attrs)+i] = log.KeyValue{Key: key, Value: slogValueToOTelValue(attr.Value)}
	}
	return &otelHandler{
		logger:   h.logger,
		attrs:    newAttrs,
		group:    h.group,
		minLevel: h.minLevel,
	}
}

func (h *otelHandler) WithGroup(name string) slog.Handler {
	newGroup := name
	if h.group != "" {
		newGroup = h.group + "." + name
	}
	return &otelHandler{
		logger:   h.logger,
		attrs:    h.attrs,
		group:    newGroup,
		minLevel: h.minLevel,
	}
}
