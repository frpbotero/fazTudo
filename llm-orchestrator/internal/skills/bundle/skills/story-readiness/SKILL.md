---
name: story-readiness
description: Prontidão de uma User Story antes de construir. Classifica cada critério de aceite pelo que o código já faz, com evidência, separa o que é decisão de produto e diz que tipo de mudança a US pede. Só leitura.
---

# Prontidão da US

Antes de alguém escrever código, responda com evidência: o que já existe, o que falta, o que ninguém
decidiu.

## Critério por critério

Para cada critério de aceite, um status e a evidência:

| Status | Quando | Evidência |
| --- | --- | --- |
| `existente` | o código já faz exatamente isso | `arquivo:linha` do comportamento e, se houver, do teste |
| `parcial` | faz parte, ou faz diferente | o que existe e o que falta |
| `ausente` | nada no código faz isso | onde você procurou |
| `ambiguo` | o critério admite mais de uma leitura | as leituras possíveis |

Procure pelo domínio (entidades, rotas, migrations, testes), não só pelo nome da US. `existente` sem
`arquivo:linha` não vale: rebaixe para `parcial`.

## Decisão de produto

Pergunta que o código e a US não respondem (regra de negócio, texto, limite, comportamento em erro)
vai em `pending_decisions`, com as alternativas e o que muda em cada uma. Não escolha por conta própria
e não reescreva a US. Detalhe técnico que o time decide não é decisão de produto.

## Tipo de mudança

- `bug`: comportamento errado a corrigir. Quem implementa vai escrever primeiro o teste que falha.
- `feature`: comportamento novo.
- `refactor`: mesmo comportamento, outra estrutura. Os testes atuais são o contrato.
- `trivial`: texto, configuração ou ajuste sem mudança de comportamento. Diga por quê em `change_reason`.

## Sinais de alerta

- Critério que depende de outra US não entregue: `block`, com a dependência.
- Critério verificável só olhando a tela: diga, para que o plano preveja E2E ou inspeção.
- Dados pessoais, pagamento, permissão: cite, a política de risco depende disso.
