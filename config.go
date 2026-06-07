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
	"fmt"
	"log/slog"
	"strings"

	"github.com/BurntSushi/toml"
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

func LoadConfig(path string) (*Config, error) {
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
