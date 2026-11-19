# Especificação: Saúde do código

**Branch**: `feat/code-health`  
**Criada**: 2026-11-19  
**Status**: Rascunho  
**Entrada**: Execução dedicada de limpeza estrutural de uma área do repositório, com o modo limpeza da
skill `structural-health`, disparada pelo console e pela interface, com áreas sugeridas por métricas
calculadas sem LLM, linha de base de lint e testes, e reprovação se o resultado piorar.

## Cenários e testes

### US1 — Limpar uma área escolhida (P1)

Uma pessoa escolhe uma área do repositório (pasta ou módulo) e inicia uma limpeza. Um agente lista os
problemas estruturais da área, redesenha as correções, aplica uma de cada vez e para quando não há mais
ganho ou o limite de rodadas acaba. O FazTudo verifica lint e testes antes e depois, e só aprova se nada
piorou. As mudanças ficam numa branch própria; a branch de base não é tocada.

**Teste independente**: em um repositório temporário com uma área que tem lógica duplicada, iniciar uma
limpeza simulada nessa área e obter uma branch própria com o registro de decisões e o veredito.

1. Com uma área válida e o repositório livre, a limpeza inicia numa branch própria criada a partir da
   branch de base, e o progresso aparece como o de uma execução de US.
2. Ao terminar sem piora de lint e testes em relação à linha de base, a execução é aprovada, com o
   resumo das mudanças por arquivo e o registro de decisões.
3. Se lint ou testes pioram em relação à linha de base, a execução é reprovada, a branch fica disponível
   para inspeção e a branch de base continua intacta.
4. Arquivos de entrada (US, EDD), arquivos protegidos e testes com ID de critério de aceite permanecem
   presentes; uma alteração proibida reprova a execução.
5. A pessoa pode parar a limpeza a qualquer momento; o que já foi feito fica na branch.

### US2 — Ver onde vale limpar (P2)

Antes de escolher, a pessoa pede ao FazTudo as áreas mais críticas. Ele calcula, sem chamar nenhum
modelo, indicadores por área e mostra as três de maior pontuação, com o motivo de cada uma.

**Teste independente**: em um repositório temporário com histórico de commits conhecido, pedir a
sugestão e obter as áreas esperadas, na ordem esperada, sem nenhuma chamada a CLI de IA.

1. A sugestão mostra até três áreas, cada uma com os indicadores que pesaram: frequência de mudança,
   arquivos que mudam juntos, ciclos de dependência e contagem de casos especiais.
2. Sem grafo de código disponível, a sugestão sai com os demais indicadores e avisa a ausência do grafo.
3. Repositório sem histórico suficiente: a sugestão avisa e a pessoa ainda pode informar a área à mão.

### US3 — Escolher opções e disparar pelo console ou pela interface (P3)

A pessoa dispara a limpeza pelo console ou pela interface, escolhe a área (sugerida ou digitada) e marca
as opções: remover comentários que só repetem o código e remover ou reescrever testes que não pegam bug.

**Teste independente**: disparar a mesma limpeza simulada pelo console e pela interface e obter execuções
equivalentes, com as opções refletidas nas instruções do agente.

1. Sem opções marcadas, comentários e testes não entram no escopo da limpeza.
2. Com cada opção marcada, a instrução correspondente chega ao agente; as demais não.
3. Console e interface oferecem as mesmas escolhas e mostram o mesmo resultado.

## Casos de borda

- Área inexistente, fora do repositório ou vazia: erro claro, sem iniciar.
- Repositório ocupado por outra execução: limpeza recusada pelo bloqueio existente.
- Linha de base já falhando: a limpeza pode rodar; a comparação é contra essas falhas, e qualquer falha
  nova reprova.
- Agente sem nenhuma correção concluída: execução termina sem mudanças, aprovada e com o registro dizendo
  por quê.
- Limite de uso do provedor durante a limpeza: mesmo failover e espera das execuções de US.
- Área que cobre o repositório inteiro: permitida só por escolha explícita, nunca como padrão.

## Requisitos funcionais

- **RF-001**: O FazTudo deve permitir iniciar uma limpeza estrutural de uma área do repositório, pelo
  console e pela interface.
- **RF-002**: O agente da limpeza deve seguir o modo limpeza da skill `structural-health` embutida no
  FazTudo, restrito à área escolhida.
- **RF-003**: A limpeza deve rodar numa branch própria, criada a partir da branch de base; a branch de
  base nunca é alterada pela limpeza.
- **RF-004**: O FazTudo deve rodar lint e testes do repositório antes (linha de base) e depois da
  limpeza, e reprovar a execução se houver falha que não existia na linha de base.
- **RF-005**: O FazTudo deve reprovar a execução se a limpeza alterar ou remover arquivo de entrada,
  arquivo protegido ou teste com ID de critério de aceite.
- **RF-006**: A limpeza deve usar o modelo pesado do provedor e ter um limite de rodadas configurável.
- **RF-007**: Remover comentários e remover ou reescrever testes só entram no escopo quando a pessoa
  marca a opção correspondente.
- **RF-008**: O FazTudo deve sugerir até três áreas críticas a partir de indicadores calculados sem
  modelo: frequência de mudança, arquivos que mudam juntos, ciclos de dependência e casos especiais.
- **RF-009**: A sugestão deve mostrar, para cada área, os indicadores que a colocaram na lista.
- **RF-010**: O registro de decisões da limpeza deve ficar disponível no resultado da execução e poder ser
  usado na descrição de um PR.
- **RF-011**: A limpeza deve funcionar com os três provedores suportados, com as mesmas permissões
  mínimas das etapas que escrevem código, e sem carregar configurações pessoais do usuário no Claude.
- **RF-012**: A limpeza deve registrar uso de tokens e custo como as demais chamadas de agente.
- **RF-013**: Mensagens, logs e instruções ao agente devem estar em português do Brasil.

### Entidades

- **Área**: caminho dentro do repositório que delimita o que a limpeza pode mudar.
- **Indicadores da área**: frequência de mudança, pares que mudam juntos, ciclos de dependência e casos
  especiais, com a pontuação resultante.
- **Execução de limpeza**: área, opções, branch, linha de base, resultado final, veredito e registro de
  decisões.
- **Registro de decisões**: lista de problemas agrupados sob as correções que os resolvem, com o estado de
  cada correção (feita, bloqueada com motivo, pendente).

## Critérios de sucesso

- **CS-001**: A sugestão de áreas sai em menos de 30 segundos num repositório de até 5.000 commits, sem
  nenhuma chamada a modelo.
- **CS-002**: Em 100% das limpezas aprovadas, lint e testes não têm falha nova em relação à linha de base.
- **CS-003**: Em 100% das execuções, a branch de base, os arquivos de entrada, os protegidos e os testes
  com ID de critério de aceite terminam intactos, ou a execução é reprovada.
- **CS-004**: Uma pessoa vai da sugestão ao início da limpeza em até três ações, no console ou na
  interface.
- **CS-005**: Os cenários acima passam no modo simulação, sem CLIs de IA instalados; `make lint` e
  `make test` passam.

## Premissas

- O disparo é manual nesta versão; sugestão automática ao fim de um épico fica para depois.
- O resultado é uma branch para revisão humana; o FazTudo não faz merge da limpeza.
- Lint e testes são os mesmos comandos que a política do repositório já define para as execuções de US.
- A área é escolhida pela pessoa; a sugestão só orienta.
- A limpeza não roda ao mesmo tempo que uma execução de US no mesmo repositório.
- Os indicadores usam o histórico do git e o grafo do graphify já usados pelo FazTudo; sem grafo, o ciclo
  de dependência fica de fora da pontuação.
