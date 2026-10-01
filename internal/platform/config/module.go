package config

import "go.uber.org/fx"

// Module provê a configuração e as suas partes.
//
// As partes são providas separadamente para que cada componente declare apenas
// o que usa: o pool depende de Database, o servidor de HTTP. Um construtor que
// recebesse Config inteira poderia ler qualquer coisa, e a dependência real
// ficaria invisível na assinatura.
var Module = fx.Module("config",
	fx.Provide(
		Load,
		func(c Config) App { return c.App },
		func(c Config) Database { return c.Database },
		func(c Config) HTTP { return c.HTTP },
		func(c Config) Reference { return c.Reference },
	),
)
