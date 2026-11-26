---
name: safe-migrations
description: Mudanças de schema e de dados sem perder dados nem derrubar o deploy. Expandir e contrair, integridade, volume, reversão e a verificação em banco descartável.
---

# Migrations seguras

## Expandir e contrair

Código antigo e novo rodam juntos durante o deploy. Por isso:

1. **Expandir**: crie a coluna, tabela ou índice novo sem quebrar quem usa o antigo (coluna nula ou com
   default, sem renomear, sem apagar).
2. **Migrar**: copie ou converta os dados, em lotes se a tabela é grande.
3. **Contrair**: só numa entrega posterior, quando nenhum código usa o antigo, remova-o.

Renomear ou apagar no mesmo passo em que o código muda quebra o deploy.

## Integridade

- Constraint nova (`NOT NULL`, `UNIQUE`, `FOREIGN KEY`) precisa que os dados existentes já obedeçam:
  confira antes, corrija os que não obedecem.
- Conversão de tipo: o que acontece com valores que não convertem?
- Dados de produção têm nulos, duplicatas e lixo que o ambiente de teste não tem. Pense neles.

## Volume e bloqueio

Tabela grande: `ALTER` que reescreve a tabela ou cria índice sem `CONCURRENTLY` pode bloquear escrita
por minutos. Diga o tamanho esperado e a estratégia.

## Reversão

Toda migration tem `down`, ou a explicação de por que não tem e como recuperar (backup, tabela de
cópia). `down` que perde dados escritos depois do `up` precisa ser dito.

## Verificação

O orquestrador roda os comandos de migration da política em banco descartável. Escreva requisitos que
eles conseguem comprovar: aplicar do zero, conferir a integridade, reverter, aplicar de novo.
