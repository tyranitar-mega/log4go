package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
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
	logger log.Logger
	attrs  []log.KeyValue
	group  string
}

func newOtelHandler(logger log.Logger) *otelHandler {
	return &otelHandler{logger: logger}
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
		logger: h.logger,
		attrs:  newAttrs,
		group:  h.group,
	}
}

func (h *otelHandler) WithGroup(name string) slog.Handler {
	newGroup := name
	if h.group != "" {
		newGroup = h.group + "." + name
	}
	return &otelHandler{
		logger: h.logger,
		attrs:  h.attrs,
		group:  newGroup,
	}
}

type OtelInstance struct {
	logger *slog.Logger
}

func NewOtelInstance(logger *slog.Logger) *OtelInstance {
	return &OtelInstance{
		logger: logger.With("component", "OtelInstance"),
	}
}

func (i *OtelInstance) Run(ctx context.Context) {
	i.logger.LogAttrs(ctx, slog.LevelInfo, "service starting",
		slog.String("event", "service.start"),
		slog.String("log_format", "json"),
	)

	i.logger.LogAttrs(ctx, slog.LevelDebug, "configuration loaded",
		slog.String("event", "config.load"),
		slog.String("otel_endpoint", "localhost:4318"),
		slog.Int("batch_size", 10),
	)

	i.logger.LogAttrs(ctx, slog.LevelWarn, "this is a warning message",
		slog.String("event", "warning.triggered"),
		slog.Float64("threshold", 0.85),
	)

	i.logger.LogAttrs(ctx, slog.LevelError, "simulated error condition",
		slog.String("event", "error.simulated"),
		slog.String("error", "connection refused"),
		slog.Int("retry_count", 3),
	)

	i.logger.LogAttrs(ctx, slog.LevelInfo, "user created",
		slog.String("event", "user.created"),
		slog.String("user_id", "usr_abc123"),
		slog.String("email", "user@example.com"),
		slog.Int("permissions", 0o755),
		slog.Duration("processing_time", 250*time.Millisecond),
	)

	i.logger.LogAttrs(ctx, slog.LevelInfo, "service stopping",
		slog.String("event", "service.stop"),
	)
}

type StdoutInstance struct {
	logger *slog.Logger
}

func NewStdoutInstance(logger *slog.Logger) *StdoutInstance {
	return &StdoutInstance{
		logger: logger.With("component", "StdoutInstance"),
	}
}

func (i *StdoutInstance) Run(ctx context.Context) {
	i.logger.InfoContext(ctx, "stdout only: starting")
	i.logger.WarnContext(ctx, "stdout only: this will not appear in OTel")
	i.logger.InfoContext(ctx, "stdout only: stopping")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName("otel-go-example"),
			semconv.ServiceVersion("0.1.0"),
			semconv.DeploymentEnvironment("development"),
		),
	)
	if err != nil {
		slog.Error("failed to create resource", "error", err)
		os.Exit(1)
	}

	exporter, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint("localhost:4317"),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		slog.Error("failed to create OTLP log exporter", "error", err)
		os.Exit(1)
	}

	processor := sdklog.NewBatchProcessor(exporter, sdklog.WithExportMaxBatchSize(10))

	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(processor),
	)
	defer loggerProvider.Shutdown(ctx)

	otelLogger := loggerProvider.Logger("otel-go-example")
	stdoutHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})

	otelInst := NewOtelInstance(slog.New(newMultiHandler(stdoutHandler, newOtelHandler(otelLogger))))
	stdoutInst := NewStdoutInstance(slog.New(stdoutHandler))

	otelInst.Run(ctx)
	stdoutInst.Run(ctx)
}
