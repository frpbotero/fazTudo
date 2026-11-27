# Contribuição

1. Leia a [constituição do projeto](../.specify/memory/constitution.md): regras de segurança, idioma e
   testes.
2. Uma branch por mudança, commits convencionais (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`).
3. Toda mudança de comportamento vem com teste no pacote. Antes do PR:
   `cd llm-orchestrator && make lint && make test`.
4. O CI roda no Ubuntu e no macOS sem nenhum CLI de agente no PATH; o PR só entra verde.
5. Mudanças só de estrutura (mover código, extrair arquivo) vão em PR próprio, sem mudar comportamento.

## Reportar problemas

Abra uma issue com versão (`faztudo -version`), sistema, o que esperava e o que aconteceu, e o trecho do
log ou do pacote de entrega. **Nunca** cole tokens, senhas ou dados pessoais. Vulnerabilidade de
segurança não vai em issue pública: fale direto com o mantenedor.

## Licença e atribuições

A licença do projeto ainda não foi definida; até lá, os direitos são do autor. As skills incorporadas
de [addyosmani/agent-skills](https://github.com/addyosmani/agent-skills) seguem a licença MIT, em
`llm-orchestrator/internal/skills/bundle/LICENSE-agent-skills`. `structural-health` é adaptada de
`untangle-codebase` (caldoai/skills, MIT). Preserve esses avisos em qualquer redistribuição.
