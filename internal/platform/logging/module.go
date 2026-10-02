package logging

import (
	"log/slog"

	"go.uber.org/fx"

	"github.com/lukspbs/jungle/internal/platform/config"
)

// Module provê o logger raiz.
var Module = fx.Module("logging",
	fx.Provide(func(cfg config.App) *slog.Logger { return New(cfg, nil) }),
)
