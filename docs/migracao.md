# Migração de configuração e de execuções salvas

## Configuração

Campos novos da política entram com o padrão: nenhuma política salva antes de uma versão precisa ser
editada para continuar funcionando. Três mudanças de comportamento valem para políticas antigas:

- **`risk_rules` ausente** usa as regras padrão, que incluem modelagem de ameaças, dados e desempenho.
  Para manter o comportamento antigo, defina `risk_rules` com as suas regras, ou `[]` para desligar.
- **QA funcional** roda em `full`, `balanced` e `audit`. Para não rodar, use `custom` com
  `"disabled_stages": ["acceptance"]`.
- **`max_parallel_agents`** passa a limitar a 4 chamadas simultâneas por execução.

## Execuções salvas (journal)

O journal ganhou versão (`schema`). Um journal antigo:

1. é carregado normalmente e continua retomável;
2. é copiado para `<arquivo>.v0.bak` antes da primeira gravação nova;
3. na retomada, roda as verificações executáveis de novo antes de promover qualquer coisa, porque não
   há manifesto que diga com o que a evidência dele foi produzida.

Um `succeeded` antigo nunca vira aprovação comprovada: sem validação registrada, a US aparece como
*não avaliada*.

## Discovery

Caches de discovery anteriores ao FT-006 não têm impressão digital e são refeitos uma vez na primeira
execução.
