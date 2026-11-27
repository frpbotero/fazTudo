# Perfil de stack: Angular, NestJS e PostgreSQL

Opcional. Vale só quando o repositório escolheu este perfil na política (`profiles`).

## Angular

- Componentes standalone, `OnPush`, estado em signals ou serviço; template sem lógica de negócio.
- Formulários reativos com validação e mensagem de erro acessível (rótulo, `aria-describedby`).
- Estados de carregando, vazio e erro em toda tela que busca dados.
- Testes de componente com o ID do critério no nome; E2E com Playwright para a jornada.

## NestJS

- Módulo por domínio. Controller fino: valida a entrada (DTO com `class-validator`) e delega ao serviço.
- Regra de negócio no serviço, sem depender do framework HTTP nem do ORM.
- Guard para autenticação e autorização; nunca confie no cliente para permissão.
- Exceção HTTP com código e mensagem do contrato. Teste do serviço com repositório falso; teste de
  integração do endpoint com banco descartável.

## PostgreSQL

- Toda mudança de schema por migration versionada, com `down`. Expandir e contrair (skill
  `safe-migrations`).
- Consulta parametrizada sempre; índice para o filtro e a ordenação usados.
- Transação para escrita que precisa ser atômica; atenção a bloqueio em tabela grande.
