# Especificação: FazTudo no console

**Branch**: `feat/console-interface`  
**Criada**: 2026-11-11  
**Status**: Implementada  
**Entrada**: CLI autônoma e TUI, começando pela CLI, com paridade funcional com as opções do painel.

## Cenários e testes

### US1 — Executar histórias sem janela (P1)

Uma pessoa informa um arquivo Markdown ou entrada padrão, escolhe todas as histórias ou um subconjunto e acompanha o pipeline no terminal. Simulação é o padrão; execução real exige uma opção explícita.

**Teste independente**: iniciar uma US simulada em um repositório temporário e obter etapas e resultado no terminal.

1. Com US válida, `run --file` inicia e exibe progresso e resultado.
2. Com `--story` ou `--stories`, só as US escolhidas são executadas, mantendo o contexto comum.
3. Com Ctrl+C ou `stop` em outro terminal, a execução é interrompida e pode ser retomada.

### US2 — Gerenciar o FazTudo pelo console (P2)

Uma pessoa consulta e ajusta repositório, branches, política, provedores e prompts, inspeciona resultados, logs e diffs, e refaz uma etapa com falha.

**Teste independente**: alterar configuração e política por arquivo JSON, reiniciar o comando e confirmar os valores salvos.

1. Consultas exibem o estado atual e aceitam JSON quando aplicável.
2. Alterações passam pelas mesmas validações usadas pelo painel e persistem no mesmo arquivo.
3. Com execução ativa, `status`, `stop` e ações de cota chegam ao processo em andamento sem servidor de rede.

### US3 — Operar por interface interativa de terminal (P3)

Uma pessoa abre `tui`, navega pelas áreas do produto e executa as mesmas operações por uma linha de comandos contextual, acompanhando a saída no próprio terminal.

**Teste independente**: abrir a TUI em um terminal, trocar de seção, consultar o estado e encerrar sem erro.

1. Tab alterna as seis seções e Enter executa o comando digitado ou a consulta padrão da seção.
2. A saída pode ser rolada e limpa sem alterar os logs persistidos.
3. Uma execução iniciada pela TUI pode ser parada nela ou em outro terminal.

## Casos de borda

- Arquivo ausente, vazio, grande demais ou sem US: erro claro, sem iniciar execução.
- Repositório ocupado: execução concorrente recusada pelo bloqueio existente.
- Processo encerrado: pedidos de controle não são enviados a um registro de atividade vencido.
- `--config ''`: não há comunicação entre processos nem retomada persistida; Ctrl+C continua disponível.

## Requisitos funcionais

- **RF-001**: A CLI deve funcionar sem janela e sem abrir porta HTTP.
- **RF-002**: A CLI deve oferecer todas as operações funcionais expostas pelo painel: execução, consulta, parada, retomada, retry, repositório, branches, política, provedores, prompts, logs e commits.
- **RF-003**: Execução real deve exigir `--real`; simulação é o padrão.
- **RF-004**: Configuração e política devem compartilhar validação e persistência com o painel.
- **RF-005**: A TUI deve oferecer acesso às mesmas operações e exibir a saída durante comandos longos.
- **RF-006**: Saídas e erros para pessoas devem estar em português do Brasil.

## Critérios de sucesso

- Os cenários acima passam sem CLIs de IA instalados no modo simulação.
- `make lint` e `make test` passam, incluindo detector de race.
- O modo servidor existente continua iniciando sem subcomando; o build desktop continua compilando.

## Premissas

- Paridade é funcional; a disposição visual do navegador não é reproduzida no terminal.
- A TUI usa seções e uma linha de comandos contextual para oferecer todas as operações.
- Os comandos de console usam o arquivo de configuração atual por padrão.
