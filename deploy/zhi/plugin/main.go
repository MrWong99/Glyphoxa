// zhi-config-glyphoxa is a zhi configuration plugin that provides default
// configuration values for a Glyphoxa Kubernetes deployment.
//
// It serves values for core settings, gateway, worker, MCP gateway,
// database, autoscaling, PostgreSQL infrastructure, and application config.
package main

import (
	"os"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/MrWong99/zhi/pkg/zhiplugin"
	"github.com/MrWong99/zhi/pkg/zhiplugin/config"
)

func main() {
	level := hclog.LevelFromString(os.Getenv("ZHI_LOG_LEVEL"))
	if level == hclog.NoLevel {
		level = hclog.Info
	}
	logger := hclog.New(&hclog.LoggerOptions{
		Name:   "zhi-config-glyphoxa",
		Level:  level,
		Output: os.Stderr,
	})
	logger.Info("starting glyphoxa config plugin")

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: zhiplugin.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"config": &config.GRPCPlugin{Impl: newGlyphoxaPlugin()},
		},
		GRPCServer: goplugin.DefaultGRPCServer,
		Logger:     logger,
	})
}
