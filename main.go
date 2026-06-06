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

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type Config struct {
	Logger LoggerConfig `toml:"logger"`
}

type LoggerConfig struct {
	Stdout        StdoutConfig `toml:"stdout"`
	OpenTelemetry OtelConfig   `toml:"opentelemetry"`
}

type StdoutConfig struct {
	Filter string `toml:"filter"`
}

type OtelConfig struct {
	Filter           string `toml:"filter"`
	OtlpGrpcEndpoint string `toml:"otlp_grpc_endpoint"`
	ExportTimeout    string `toml:"export_timeout"`
}

func loadConfig(path string) (*Config, error) {
	var config Config
	if _, err := toml.DecodeFile(path, &config); err != nil {
		return nil, fmt.Errorf("decoding config file: %w", err)
	}
	return &config, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

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
		slog.String("otel_endpoint", "localhost:4317"),
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

func Run(configPath string) error {
	config, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var handlers []slog.Handler
	var otelProvider *sdklog.LoggerProvider

	if config.Logger.Stdout.Filter != "" {
		level := parseLevel(config.Logger.Stdout.Filter)
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
		handlers = append(handlers, handler)
	}

	if config.Logger.OpenTelemetry.Filter != "" {
		endpoint := config.Logger.OpenTelemetry.OtlpGrpcEndpoint
		endpoint = strings.TrimPrefix(endpoint, "http://")
		endpoint = strings.TrimPrefix(endpoint, "https://")
		if endpoint == "" {
			endpoint = "localhost:4317"
		}

		var exportTimeout time.Duration
		if config.Logger.OpenTelemetry.ExportTimeout != "" {
			exportTimeout, err = time.ParseDuration(config.Logger.OpenTelemetry.ExportTimeout)
			if err != nil {
				return fmt.Errorf("parsing export_timeout: %w", err)
			}
		}

		res, err := resource.New(ctx,
			resource.WithAttributes(
				semconv.ServiceName("otel-go-example"),
				semconv.ServiceVersion("0.1.0"),
				semconv.DeploymentEnvironment("development"),
			),
		)
		if err != nil {
			return fmt.Errorf("creating resource: %w", err)
		}

		var expOpts []otlploggrpc.Option
		expOpts = append(expOpts, otlploggrpc.WithEndpoint(endpoint), otlploggrpc.WithInsecure())
		if exportTimeout > 0 {
			expOpts = append(expOpts, otlploggrpc.WithTimeout(exportTimeout))
		}

		exporter, err := otlploggrpc.New(ctx, expOpts...)
		if err != nil {
			return fmt.Errorf("creating OTLP log exporter: %w", err)
		}

		var procOpts []sdklog.BatchProcessorOption
		procOpts = append(procOpts, sdklog.WithExportMaxBatchSize(10))
		if exportTimeout > 0 {
			procOpts = append(procOpts, sdklog.WithExportTimeout(exportTimeout))
		}
		processor := sdklog.NewBatchProcessor(exporter, procOpts...)

		otelProvider = sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(processor),
		)

		otelLogger := otelProvider.Logger("otel-go-example")
		minLevel := parseLevel(config.Logger.OpenTelemetry.Filter)
		otelHandler := newOtelHandler(otelLogger, minLevel)
		handlers = append(handlers, otelHandler)
	}

	if otelProvider != nil {
		defer func() {
			otelProvider.Shutdown(context.Background())
		}()
	}

	if len(handlers) == 0 {
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
		handlers = append(handlers, handler)
	}

	var logger *slog.Logger
	if len(handlers) == 1 {
		logger = slog.New(handlers[0])
	} else {
		logger = slog.New(newMultiHandler(handlers...))
	}

	slog.SetDefault(logger)

	otelInst := NewOtelInstance(logger)
	otelInst.Run(ctx)
	stdoutInst := NewStdoutInstance(logger)
	stdoutInst.Run(ctx)

	return nil
}

func main() {
	configPath := "config.toml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	if err := Run(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
