---
name: test-quality
description: Avalia se os testes de uma mudança comprovam o comportamento. Teste que falharia sem a mudança, cenários negativos, independência, nomes com o ID do critério, sem mocks que testam o mock.
---

# Qualidade dos testes

Um teste vale pelo que ele pegaria se o código estivesse errado.

## Perguntas por teste

1. **Falharia sem a mudança?** Em correção de bug, o teste precisa falhar no código antigo pelo
   comportamento errado (o orquestrador confere). Teste que só exercita o código, sem verificar o
   resultado, não pega nada.
2. **Verifica o comportamento, não a implementação?** Asserção sobre chamada interna, ordem de
   execução ou estrutura privada quebra na refatoração e não protege o usuário.
3. **Cobre o negativo?** Entrada inválida, sem permissão, vazio, limite, erro do serviço chamado.
4. **É independente?** Não depende da ordem, do relógio, da rede, de dados de outro teste.
5. **O nome diz o critério?** Com o ID do caso da especificação (ex.: `UT-CART-01`), para a
   rastreabilidade ligar o caso executado ao critério.

## Sinais de teste fraco

- `expect(true)`, asserção ausente ou só `not.toThrow` onde há resultado a conferir.
- Mock do próprio código sob teste.
- Teste ignorado (`skip`, `xit`, `t.Skip`) sem motivo: não comprova nada e o orquestrador cobra.
- Expectativa ajustada para o que o código faz em vez do que a especificação pede.
- Um teste gigante que verifica dez coisas: quando falha, não diz qual.

## O que não é problema

Estilo do teste diferente do seu gosto, mas igual ao do projeto. Siga a convenção do repositório.
