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
	"time"

	"github.com/tyranitar-mega/log4go"
)

func main() {
	configPath := "config.toml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := log4go.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger, shutdown, err := log4go.NewLogger(ctx, cfg,
		log4go.WithServiceName("log4go-example"),
		log4go.WithServiceVersion("0.1.0"),
		log4go.WithDeploymentEnvironment("development"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer shutdown()

	logger.LogAttrs(ctx, slog.LevelInfo, "service starting",
		slog.String("event", "service.start"),
		slog.String("log_format", "json"),
	)

	logger.LogAttrs(ctx, slog.LevelDebug, "configuration loaded",
		slog.String("event", "config.load"),
		slog.String("otel_endpoint", "localhost:4317"),
		slog.Int("batch_size", 10),
	)

	logger.LogAttrs(ctx, slog.LevelWarn, "this is a warning message",
		slog.String("event", "warning.triggered"),
		slog.Float64("threshold", 0.85),
	)

	logger.LogAttrs(ctx, slog.LevelError, "simulated error condition",
		slog.String("event", "error.simulated"),
		slog.String("error", "connection refused"),
		slog.Int("retry_count", 3),
	)

	logger.LogAttrs(ctx, slog.LevelInfo, "user created",
		slog.String("event", "user.created"),
		slog.String("user_id", "usr_abc123"),
		slog.String("email", "user@example.com"),
		slog.Int("permissions", 0o755),
		slog.Duration("processing_time", 250*time.Millisecond),
	)

	logger.LogAttrs(ctx, slog.LevelInfo, "service stopping",
		slog.String("event", "service.stop"),
	)
}
