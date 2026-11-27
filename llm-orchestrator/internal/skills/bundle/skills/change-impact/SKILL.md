---
name: change-impact
description: Análise de impacto para o plano de uma US. Mapeia contratos, consumidores, migrations e recursos compartilhados além dos arquivos editados, diz como reverter e transforma critérios em cenários verificáveis. Só leitura.
---

# Impacto da mudança

O plano não é só a lista de arquivos. Pergunte o que mais quebra se isto mudar.

## Contratos e consumidores

- Contrato: endpoint, evento, mensagem, schema, tipo exportado, arquivo de configuração lido por outro.
- Consumidor: quem chama ou lê esse contrato — outro serviço, o frontend, um job, um cliente externo.
- Mudou um contrato? Liste cada consumidor e se ele continua funcionando sem mudar junto.
  Incompatível: proponha a versão nova ao lado da antiga, não a troca.

## Migrations e dados

- Toda mudança de schema ou de dados existentes vai em `impact.migrations`.
- Durante o deploy, código antigo e novo convivem com o mesmo banco: a migration precisa servir aos dois.
- `rollback` diz como desfazer ou, se não dá, como recuperar.

## Recursos compartilhados

Banco, fila, cache, porta, serviço externo, arquivo de lock. Frentes que tocam o mesmo recurso não rodam
em paralelo: liste-o em `resources` da frente.

## Cenários de aceitação

Um cenário por critério, em dado/quando/então, com o método que comprova (`teste unitário`,
`integração`, `e2e`, `inspeção`). Inclua os negativos: entrada inválida, sem permissão, vazio, erro do
serviço. Método `inspeção` só quando nenhum teste executável consegue comprovar.

## Requisitos não funcionais

Só os que a US pede. Meta mensurável (`p95 < 200 ms`, `1000 req/s`) ou vazio: nunca invente número.
