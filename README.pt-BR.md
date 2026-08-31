# Cliente de referência — diretório Helppi (SCIM 2.0)

*English version: [README.md](README.md).*

Implementação de referência, em Go, do cliente de sincronização descrito na
**proposta técnica do diretório de parceiros da Helppi, seções 06 a 08 e Apêndice A**.

Não é um SDK e não precisa ser adotado como está. É código que compila, roda e
tem testes, escrito para que as duas engenharias discutam comportamento em cima
de algo concreto — e para que o time do the partner tenha um ponto de partida em vez
de uma especificação em prosa.

- **Sem dependências.** Só a biblioteca padrão do Go (1.22+). Dá para copiar os
  pacotes para dentro do repositório de vocês e compilar offline.
- **Testes contra um diretório falso** que implementa o mesmo contrato,
  incluindo os erros previstos (`403`, `409`, `429`, `5xx`).
- **`testdata/directory.json` é o conjunto de conformidade**: os cinco estados de
  ciclo de vida da proposta, mais os casos de devolução do `picker_id`. Os dois
  lados podem testar contra os mesmos bytes.

```
go test ./... -race        # 21 testes, ~2s, sem rede
make build                 # bin/directorysyncd
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… make run-once
```

## O modelo: reconciliador, não consumidor de eventos

Cada ciclo recalcula o estado desejado a partir do diretório e converge. Não há
ordem a preservar nem evento a perder, então o processo pode ser morto e
reiniciado em qualquer ponto.

```
a cada 5 min ──► ciclo incremental ──► apply(registro) ──► devolve picker_id
                        │                                        │
              checkpoint só avança se o ciclo             409 ⇒ alerta,
              terminou inteiro                            nunca repetição
a cada 24 h ─► varredura completa ──► relatório de divergência
```

## Estrutura

| Pacote | Responsabilidade |
|---|---|
| `scim` | Protocolo: tipos, cliente HTTP, paginação, retry. Não sabe o que é um picker. |
| `store` | Interface do lado local (pickers e checkpoint) + implementação em memória. |
| `directory` | O reconciliador. Não sabe o que é HTTP. |
| `scimtest` | Diretório falso com injeção de falhas. |
| `cmd/directorysyncd` | O worker: dois tickers, logs estruturados, `/metrics` e `/healthz`. |
| `deploy/schema.sql` | Esquema de referência em Postgres. |

Essa separação é o que permite testar o reconciliador contra um diretório falso
e o cliente contra respostas fixas, sem subir nada.

## As sete decisões que valem discussão

1. **`Active` é `*bool`, não `bool`.** Com `bool`, uma resposta truncada ou um
   atributo ausente decodifica para `false` — e o reconciliador desabilita a
   frota inteira. `nil` é erro, nunca "desabilitado".
2. **O checkpoint só avança quando o ciclo termina inteiro.** Um ciclo parcial
   que avança a marca d'água perde registros de forma permanente e silenciosa: o
   sintoma aparece semanas depois, como um picker que ninguém bloqueou.
3. **A marca d'água vem de `meta.lastModified`, não do relógio local.** Assim o
   desvio de relógio entre as duas empresas deixa de importar.
4. **Dois minutos de sobreposição** são relidos a cada ciclo, para absorver
   corridas de visibilidade do lado do diretório. É seguro porque `apply` é
   idempotente — que é a razão de insistir em idempotência.
5. **Correspondência exclusivamente pelo `id` do diretório.** Nunca por login,
   nome ou alias, em nenhuma etapa, nem na carga inicial.
6. **`409` na devolução do `picker_id` não é repetido.** Significa que o
   mapeamento local está errado; repetir não corrige e um laço de retentativa
   esconde o problema. Vira alerta.
7. **Ausência no diretório nunca desprovisiona.** A varredura diária *reporta* a
   divergência e para por aí: o desligamento chega sempre explícito, como
   `active: false`, antes de o registro sumir.

## O que trocar antes de usar em produção

- `store/memory` → sua implementação de `store.Store`. O contrato está
  em `store/store.go`; o esquema de referência em `deploy/schema.sql`.
  O índice único em `directory_id` é o que garante idempotência na criação — a
  verificação em Go é caminho rápido, não garantia.
- `Options.Alert` → o canal de plantão de vocês.
- `cmd/directorysyncd/metrics.go` → o registrador de métricas de vocês.
  `directory_sync_lag_seconds` é a métrica que importa: é o SLI por trás de "um
  desligamento chega ao the partner em até N minutos", e alerta sobre um worker
  travado mesmo quando nada está dando erro.
- **Rode uma instância só.** Duas réplicas no mesmo cronograma tentam criar os
  mesmos pickers na primeira sincronização. O índice único evita duplicidade,
  mas a tempestade de `409` é evitável: use *lease*, *advisory lock* ou
  `concurrencyPolicy: Forbid`.

## Cobertura dos testes

| Cenário | Teste |
|---|---|
| Paginação até a última página | `TestListUsersWalksEveryPage` |
| Consulta incremental por data | `TestListUsersHonoursFilter` |
| Atributo `active` ausente vira `nil` | `TestActiveIsNilWhenAbsent` |
| `429` respeita `Retry-After`; `5xx` faz backoff | `TestRetriesOn429AndHonoursRetryAfter` |
| `401` não é repetido | `TestCredentialErrorIsNotRetried` |
| Corpo do `PATCH` conforme o contrato | `TestPatchExternalIDSendsTheContractualBody` |
| Primeiro ciclo cria e devolve o `picker_id` | `TestFirstCycleCreatesActivePickersAndWritesBack` |
| Segundo ciclo não faz nada | `TestSecondCycleIsANoOp` |
| Suspensão e reativação reusam o mesmo picker | `TestSuspensionThenReactivation` |
| Checkpoint vem do diretório | `TestCheckpointComesFromDirectoryTimestamps` |
| Registro malformado aborta o ciclo e segura o checkpoint | `TestMissingActiveFlagAbortsCycleAndHoldsCheckpoint` |
| Ciclo que falhou é refeito do mesmo ponto | `TestFailedCycleIsRetriedFromTheSameCheckpoint` |
| O eco da própria escrita é inofensivo | `TestPartnerWriteBackEchoIsHarmless` |
| Divergência é reportada, não aplicada | `TestFullReportsDriftWithoutDeprovisioning` |
| Varredura completa pega o que o incremental perdeu | `TestFullDetectsAPickerTheIncrementalPathMissed` |
| Conflito na devolução alerta sem derrubar o ciclo | `TestWriteBackConflictAlertsAndDoesNotFailTheCycle` |
| Corrida na criação não duplica picker | `TestCreateRaceFallsBackToTheExistingPicker` |

Esses cenários são exatamente os critérios de aceite das **Fases 1 e 2** da
proposta. Se o cliente de vocês passar por eles contra o ambiente de teste da
Helppi, a Fase 1 está concluída.

## Fora de escopo

O acesso com um toque (seções 09 a 11 da proposta) não está aqui. É um cliente
OpenID Connect comum — `golang.org/x/oauth2` mais `github.com/coreos/go-oidc` —
em que o callback faz `sub → pickers.directory_id → sessão`. O caminho
alternativo, com URL de entrada assinada, é uma verificação de JWT mais um
registro de uso único.
