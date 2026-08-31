# helppi-scim-go

Cliente de referência, em Go, para o diretório de parceiros da Helppi — a
integração SCIM 2.0 descrita nas seções 06 a 08 e no Apêndice A da
*proposta técnica do diretório de parceiros da Helppi*.

*English version: [README.md](README.md).*

Não é um SDK e não precisa ser adotado como está. É código que compila, roda e
tem testes, escrito para que as duas engenharias discutam comportamento em cima
de algo concreto em vez de prosa — e para que o parceiro comece de um
reconciliador funcionando, não de uma especificação.

- **Sem dependências.** Só a biblioteca padrão do Go (1.22+). Dá para copiar os
  pacotes para dentro do repositório de vocês e compilar offline.
- **Testado contra um diretório falso** que implementa o mesmo contrato,
  incluindo as falhas que importam: `403`, `409`, `429`, `5xx`, registros
  inválidos, paginação quebrada e respostas que nem SCIM são.
- **`testdata/directory.json` é o conjunto de conformidade**: os cinco estados
  de ciclo de vida da proposta, mais os casos de devolução do `picker_id`. Os
  dois lados podem testar contra os mesmos bytes.

```bash
go test ./... -race        # 39 testes, sem rede
make ci                    # gofmt + vet + testes
```

## Comece por uma execução seca

```bash
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… make dry-run
```

A execução seca relata o que um ciclo faria e não escreve nada — nem no banco de
vocês, nem no diretório, nem no checkpoint.

O worker **se recusa** a rodar de verdade contra um diretório usando o store em
memória, e essa trava é deliberada: um store vazio faz todo registro parecer
novo, então ele criaria pickers do zero e gravaria esses ids inventados por cima
dos `picker_id` reais. Isso corrompe dados do lado da Helppi. Ligue um
`store.Store` de verdade antes.

## Início rápido

A única coisa que vocês precisam escrever é uma implementação de `store.Store`.

```go
client, err := scim.New(scim.Options{BaseURL: url, Token: token})
if err != nil {
    return err
}

syncer := directory.New(client, meuStore, directory.Options{})

// Um ciclo. Chame de onde vocês agendam trabalho.
stats, err := syncer.Incremental(ctx)
```

Depois prove que o store cumpre o contrato — inclusive as regras que a
assinatura dos métodos não consegue expressar:

```go
func TestMeuStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) store.Store { return novoStoreDeTeste(t) })
}
```

## O modelo: reconciliador, não consumidor de eventos

Cada ciclo recalcula o estado desejado a partir do diretório e converge. Não há
ordem a preservar nem evento a perder, então o processo pode ser morto e
reiniciado em qualquer ponto.

```
a cada 5 min ──► ciclo incremental ──► apply(registro) ──► devolve picker_id
                        │                                        │
              checkpoint só avança se o ciclo             409 ⇒ alerta uma vez,
              terminou inteiro                            nunca repetição
a cada 24 h ─► varredura completa ──► relatório de divergência
```

## Estrutura

| Pacote | Responsabilidade |
|---|---|
| `scim` | Protocolo: tipos, cliente HTTP, paginação, retry. Não sabe o que é um picker. |
| `store` | O contrato do lado local, com implementação em memória e suíte de contrato. |
| `directory` | O reconciliador. Não sabe o que é HTTP. |
| `scimtest` | Diretório falso com injeção de falhas. |
| `cmd/directorysyncd` | O worker: dois tickers, logs estruturados, `/metrics`, `/healthz`, `/readyz`. |

## Documentação

| Documento | O que responde |
|---|---|
| [docs/INTEGRATION.md](docs/INTEGRATION.md) | O contrato: modelo de identidade, ciclo de vida, matriz de erros e qual teste defende cada promessa. |
| [docs/IMPLEMENTING_STORE.md](docs/IMPLEMENTING_STORE.md) | Como escrever a única interface que é de vocês, e as duas linhas do schema que sustentam tudo. |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Métricas, limiares de alerta e runbook por tipo de falha. |

## Nove decisões que valem discussão

1. **`Active` é `*bool`, não `bool`.** Com `bool`, uma resposta truncada ou um
   atributo ausente decodifica para `false` — e o reconciliador desabilita a
   frota inteira. `nil` é recusado, nunca lido como "desabilitado".
2. **O checkpoint só avança quando o ciclo termina inteiro.** Um ciclo parcial
   que avança a marca d'água perde registros de forma permanente e silenciosa: o
   sintoma aparece semanas depois, como um picker que ninguém bloqueou.
3. **A marca d'água vem de `meta.lastModified`, não do relógio local.** O desvio
   de relógio entre as duas empresas deixa de importar. Um horário absurdamente
   no futuro é recusado em vez de aceito.
4. **Dois minutos de sobreposição** são relidos a cada ciclo, para absorver
   corridas de visibilidade. É seguro porque aplicar um registro é idempotente —
   que é a razão de insistir em idempotência.
5. **Registro inválido é pulado, não fatal — mas a marca d'água fica atrás
   dele.** Falhar o ciclo congelaria o checkpoint e pararia a frota inteira por
   causa de uma linha ruim; pular sem segurar a marca d'água perderia a correção
   futura dela. Só uma enxurrada deles falha o ciclo.
6. **A correspondência é exclusivamente pelo `id` do diretório.** Nunca por
   login, nome ou alias, em nenhuma etapa, nem na carga inicial.
7. **`409` na devolução nunca é repetido, e alerta uma vez por identidade.**
   Significa que o mapeamento local está errado; repetir não corrige, e alertar
   a cada cinco minutos ensina as pessoas a ignorar o alerta.
8. **Ausência no diretório nunca desprovisiona.** A varredura diária *reporta* a
   divergência e para por aí: o desligamento chega sempre explícito, como
   `active: false`, antes de o registro sumir.
9. **Resposta que não é SCIM é erro, não diretório vazio.** Uma página HTML de
   bloqueio, decodificada com folga, vira "não trabalha mais ninguém aqui".

## O que trocar antes de produção

- `store/memory` → o `store.Store` de vocês, verificado com `storetest.Run`. O
  índice único em `directory_id` é o que garante idempotência na criação — a
  verificação em Go é caminho rápido, não garantia.
- `Options.Alert` → o canal de plantão de vocês.
- `cmd/directorysyncd/metrics.go` → o registrador de métricas de vocês.
  `directory_sync_lag_seconds` é a métrica que importa: é o SLI por trás de "um
  desligamento chega ao parceiro em até N minutos", e pega um worker travado
  mesmo quando nada está dando erro.
- **Rode uma instância só.** Duas réplicas no mesmo cronograma tentam criar os
  mesmos pickers na primeira sincronização. O índice único evita duplicidade,
  mas a tempestade de `409` é evitável: use *lease*, *advisory lock* ou
  `concurrencyPolicy: Forbid`.

## Fora de escopo

O acesso com um toque (seções 09 a 11 da proposta) não está aqui. É um cliente
OpenID Connect comum — `golang.org/x/oauth2` mais `github.com/coreos/go-oidc` —
em que o callback faz `sub → pickers.directory_id → sessão`. O caminho
alternativo, com URL de entrada assinada, é uma verificação de JWT mais um
registro de uso único.

## Licença

MIT. Veja [LICENSE](LICENSE).
