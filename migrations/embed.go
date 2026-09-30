// Package migrations embute as migrations SQL no binário.
//
// Embutir em vez de copiar arquivos para a imagem torna o binário
// autossuficiente: o mesmo artefato aplica o schema em produção, no Docker
// Compose e nos testes de integração, sem risco de divergência entre o SQL
// versionado e o SQL que efetivamente roda.
package migrations

import "embed"

// FS contém os arquivos .up.sql e .down.sql versionados.
//
//go:embed *.sql
var FS embed.FS
