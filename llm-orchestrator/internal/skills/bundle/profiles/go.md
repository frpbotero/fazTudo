# Perfil de stack: Go

Opcional. Vale só quando o repositório escolheu este perfil na política (`profiles`).

- **Testes**: `go test ./...` com `-race` onde houver concorrência. Tabela de casos com `t.Run(nome)`,
  ID do critério no nome do subteste (`t.Run("UT-CART-01 adiciona item", …)`). `go test -json` dá o
  relatório por caso que liga o teste ao critério.
- **Erros**: retorne `error`, envolva com `fmt.Errorf("contexto: %w", err)`, compare com `errors.Is` e
  `errors.As`. Sem `panic` para fluxo normal.
- **Contexto**: `context.Context` como primeiro parâmetro de quem faz I/O ou pode demorar; respeite o
  cancelamento.
- **Concorrência**: quem inicia uma goroutine garante que ela termina. Mutex protege dados, não código.
- **Pacotes**: nomes curtos, sem `util`/`common`. Interfaces pequenas, definidas por quem consome.
- **Formatação**: `gofmt` e `go vet` limpos. Sem dependência nova sem confirmar no `go.mod`.
