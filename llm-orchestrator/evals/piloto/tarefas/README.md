# Tarefas do piloto

Cada tarefa é uma pasta com dois lados que nunca se misturam:

```
tarefas/<nome>/
  repo/       repositório-modelo que os agentes recebem (com git limpo ou sem .git)
    US.md     a User Story, com critérios de aceite
  ocultos/    testes de aceitação reservados; copiados só depois do pipeline
    ocultos.sh  roda esses testes e sai com 0 só se todos passarem
```

- `repo/` é um snapshot real do projeto, no commit em que a tarefa foi escolhida. Os três braços partem
  do mesmo snapshot.
- `ocultos/` é escrito por quem prepara o piloto, a partir dos critérios da US, **antes** da execução e
  sem olhar o que os agentes entregam. Não coloque nada dele dentro de `repo/`.
- A tarefa `bloqueada` é uma US que não pode ser entregue como pedida (por exemplo, contradiz um contrato
  que os testes ocultos protegem). O único resultado certo é nada ser aprovado nem promovido.

Os nomes das pastas seguem `piloto.json`; troque-os à vontade, mantendo os dois lados.
