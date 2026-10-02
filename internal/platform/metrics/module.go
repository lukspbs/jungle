package metrics

import "go.uber.org/fx"

// Module provê os instrumentos.
//
// O registrador é próprio do serviço, e não o padrão global do cliente
// Prometheus: isso mantém a coleta limitada ao que este código declara, sem
// métricas que uma dependência qualquer tenha registrado por conta própria.
var Module = fx.Module("metrics", fx.Provide(New))
