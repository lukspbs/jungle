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
