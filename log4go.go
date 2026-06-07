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
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type LoggerOption func(*loggerOptions)

type loggerOptions struct {
	serviceName    string
	serviceVersion string
	deploymentEnv  string
}

func WithServiceName(name string) LoggerOption {
	return func(o *loggerOptions) {
		o.serviceName = name
	}
}

func WithServiceVersion(version string) LoggerOption {
	return func(o *loggerOptions) {
		o.serviceVersion = version
	}
}

func WithDeploymentEnvironment(env string) LoggerOption {
	return func(o *loggerOptions) {
		o.deploymentEnv = env
	}
}

func NewLogger(ctx context.Context, cfg *Config, opts ...LoggerOption) (*slog.Logger, func(), error) {
	var options loggerOptions
	for _, opt := range opts {
		opt(&options)
	}
	if options.serviceName == "" {
		options.serviceName = "log4go"
	}
	if options.serviceVersion == "" {
		options.serviceVersion = "0.1.0"
	}
	if options.deploymentEnv == "" {
		options.deploymentEnv = "development"
	}

	var handlers []slog.Handler
	var otelProvider *sdklog.LoggerProvider

	if cfg.Logger.Stdout.Filter != "" {
		level := parseLevel(cfg.Logger.Stdout.Filter)
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
		handlers = append(handlers, handler)
	}

	if cfg.Logger.OpenTelemetry.Filter != "" {
		endpoint := cfg.Logger.OpenTelemetry.OtlpGrpcEndpoint
		endpoint = strings.TrimPrefix(endpoint, "http://")
		endpoint = strings.TrimPrefix(endpoint, "https://")
		if endpoint == "" {
			endpoint = "localhost:4317"
		}

		var exportTimeout time.Duration
		var err error
		if cfg.Logger.OpenTelemetry.ExportTimeout != "" {
			exportTimeout, err = time.ParseDuration(cfg.Logger.OpenTelemetry.ExportTimeout)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing export_timeout: %w", err)
			}
		}

		res, err := resource.New(ctx,
			resource.WithAttributes(
				semconv.ServiceName(options.serviceName),
				semconv.ServiceVersion(options.serviceVersion),
				semconv.DeploymentEnvironment(options.deploymentEnv),
			),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("creating resource: %w", err)
		}

		var expOpts []otlploggrpc.Option
		expOpts = append(expOpts, otlploggrpc.WithEndpoint(endpoint), otlploggrpc.WithInsecure())
		if exportTimeout > 0 {
			expOpts = append(expOpts, otlploggrpc.WithTimeout(exportTimeout))
		}

		exporter, err := otlploggrpc.New(ctx, expOpts...)
		if err != nil {
			return nil, nil, fmt.Errorf("creating OTLP log exporter: %w", err)
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

		otelLogger := otelProvider.Logger(options.serviceName)
		minLevel := parseLevel(cfg.Logger.OpenTelemetry.Filter)
		otelHandler := newOtelHandler(otelLogger, minLevel)
		handlers = append(handlers, otelHandler)
	}

	shutdown := func() {
		if otelProvider != nil {
			otelProvider.Shutdown(context.Background())
		}
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

	return logger, shutdown, nil
}
