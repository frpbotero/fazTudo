# Contrato: prompt do agente de limpeza

Montado por `buildCleanupPrompt`, na ordem (contexto estável primeiro, para o cache):

1. Repositório, stacks, discovery (cache) e grafo, como nas US.
2. Instruções do projeto.
3. `## Skills`: `structural-health` e `incremental-implementation` (caminhos no pacote embutido).
4. `## Limpeza`:
   - `Modo: limpeza` da skill `structural-health`.
   - `Área: <caminho>`; só arquivos dentro dela podem mudar; mudança que afetaria quem está fora vira
     apontamento no registro.
   - `Rodadas: <n>`.
   - `Registro de decisões: <caminho em faztudo-out/>`.
   - `Comentários: sim|não`, `Testes: sim|não`.
   - Arquivos protegidos e de entrada da política, listados.
   - Comandos permitidos (os da política), para checar cada fatia.
5. `## Sua tarefa`: texto personalizável da etapa (catálogo de prompts, grupo "Saúde do código").

Saída esperada: o formato da skill (mudanças por arquivo, uma linha cada; correções bloqueadas com motivo;
caminho do registro). O orquestrador não interpreta a saída para decidir o veredito: decide só pelos
comandos, pelo guard, pela área e pelos IDs de teste.
