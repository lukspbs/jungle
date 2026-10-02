// Package guards reúne verificações estruturais sobre o próprio código-fonte.
package guards

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tiposProibidos são os tipos de ponto flutuante. Dinheiro não pode encostar
// neles em nenhuma etapa: parsing, cálculo, serialização ou persistência.
var tiposProibidos = map[string]bool{
	"float32": true,
	"float64": true,
}

// funcoesProibidas são as portas de entrada mais comuns para um float aparecer
// sem que o tipo seja escrito explicitamente.
var funcoesProibidas = map[string]bool{
	"ParseFloat": true,
	"Float64":    true,
	"Float32":    true,
}

// excecoes lista os arquivos autorizados a usar ponto flutuante, cada um com o
// motivo.
//
// A lista existe porque a regra que importa é "dinheiro nunca toca float", e
// não "o módulo nunca menciona float". Um arquivo que lida com durações e
// contagens, e que comprovadamente não enxerga valor monetário, pode usá-lo.
//
// A autorização não é na palavra: TestExcecoesNaoAlcancamDinheiro confere que
// nenhum arquivo isento importa o pacote money, nem direta nem indiretamente
// pelos pacotes de domínio. É isso que impede a lista de virar uma porta.
var excecoes = map[string]string{
	"internal/platform/metrics/metrics.go": "a API do cliente Prometheus é float64: " +
		"histogramas, gauges e Observe não aceitam outro tipo. O pacote mede " +
		"durações e contagens, nunca valores monetários.",
}

// TestNenhumPontoFlutuanteNoCodigo varre a AST de todo o módulo. É um critério
// eliminatório do desafio, então vale uma trava automática em vez de revisão
// manual. A varredura é sobre identificadores, não sobre texto: por isso este
// arquivo pode citar "float64" em string sem se autoacusar.
func TestNenhumPontoFlutuanteNoCodigo(t *testing.T) {
	raiz := raizDoModulo(t)
	esteArquivo := arquivoDesteTeste(t)

	fset := token.NewFileSet()
	var violacoes []string

	err := filepath.WalkDir(raiz, func(caminho string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(caminho, ".go") || caminho == esteArquivo {
			return nil
		}

		arquivo, err := parser.ParseFile(fset, caminho, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}

		relativo, err := filepath.Rel(raiz, caminho)
		if err != nil {
			relativo = caminho
		}
		if _, isento := excecoes[filepath.ToSlash(relativo)]; isento {
			return nil
		}

		ast.Inspect(arquivo, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if funcoesProibidas[node.Sel.Name] {
					violacoes = append(violacoes, posicao(fset, relativo, node.Sel.Pos(), node.Sel.Name))
				}
			case *ast.Ident:
				if tiposProibidos[node.Name] {
					violacoes = append(violacoes, posicao(fset, relativo, node.Pos(), node.Name))
				}
			case *ast.BasicLit:
				if node.Kind == token.FLOAT {
					violacoes = append(violacoes, posicao(fset, relativo, node.Pos(), "literal de ponto flutuante "+node.Value))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("falha ao varrer o módulo: %v", err)
	}

	if len(violacoes) > 0 {
		t.Fatalf("ponto flutuante encontrado em %d ponto(s):\n  %s",
			len(violacoes), strings.Join(violacoes, "\n  "))
	}
}

func posicao(fset *token.FileSet, arquivo string, pos token.Pos, o string) string {
	return arquivo + ":" + itoa(fset.Position(pos).Line) + ": " + o
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// raizDoModulo sobe a partir do diretório do pacote até encontrar o go.mod.
func raizDoModulo(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("não foi possível obter o diretório de trabalho: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			t.Fatal("go.mod não encontrado a partir do diretório de trabalho")
		}
		dir = pai
	}
}

func arquivoDesteTeste(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("não foi possível obter o diretório de trabalho: %v", err)
	}
	return filepath.Join(dir, "nofloat_test.go")
}

// TestExcecoesNaoAlcancamDinheiro é o que torna a lista de exceções segura.
//
// Um arquivo autorizado a usar ponto flutuante não pode enxergar valor
// monetário. A checagem é sobre os imports: se o arquivo não conhece o pacote
// money nem os pacotes de domínio que o carregam, não há como um valor
// monetário chegar até ele para ser convertido.
func TestExcecoesNaoAlcancamDinheiro(t *testing.T) {
	if len(excecoes) == 0 {
		t.Skip("nenhuma exceção declarada")
	}

	raiz := raizDoModulo(t)
	proibidos := []string{
		"github.com/lukspbs/jungle/internal/domain/money",
		"github.com/lukspbs/jungle/internal/domain/wallet",
		"github.com/lukspbs/jungle/internal/domain/wagering",
		"github.com/lukspbs/jungle/internal/domain/events",
	}

	fset := token.NewFileSet()
	for relativo, motivo := range excecoes {
		caminho := filepath.Join(raiz, filepath.FromSlash(relativo))

		if _, err := os.Stat(caminho); err != nil {
			t.Errorf("exceção declarada para arquivo inexistente: %s", relativo)
			continue
		}
		if strings.TrimSpace(motivo) == "" {
			t.Errorf("exceção sem motivo declarado: %s", relativo)
		}

		arquivo, err := parser.ParseFile(fset, caminho, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("%s: %v", relativo, err)
			continue
		}

		for _, imp := range arquivo.Imports {
			caminhoImportado := strings.Trim(imp.Path.Value, `"`)
			for _, proibido := range proibidos {
				if caminhoImportado == proibido {
					t.Errorf("%s está isento da trava de ponto flutuante mas importa %s: "+
						"um valor monetário poderia chegar até ele", relativo, proibido)
				}
			}
		}
	}
}
