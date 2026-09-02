# helppi-scim-go

[![CI](https://github.com/Helppi/helppi-scim-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Helppi/helppi-scim-go/actions/workflows/ci.yml)

**Um cliente pronto para o diretório de parceiros da Helppi** — o código que
mantém o cadastro de profissionais do seu lado em dia com o nosso,
automaticamente.

> **Como ler.** A Parte 1 foi escrita para produto e operação: o que é isto, o
> que muda quando roda, e o que o time de vocês precisa fazer. A Parte 2 é a
> referência de engenharia. Não é preciso ler a Parte 2 para decidir se vale a
> pena.

---

# Parte 1 · O que é isto

## Em um parágrafo

A Helppi publica um diretório com os profissionais autorizados a trabalhar com
vocês. Este repositório é um **cliente de referência para esse diretório**: um
programa pequeno que pergunta à Helppi "quem existe e quem pode operar agora?",
aplica a resposta às contas de vocês e devolve o identificador que vocês geraram,
de modo que os dois lados passem a compartilhar uma chave permanente. É escrito
em Go, não tem dependências, e vem com uma bateria de testes que comprova a
integração antes de ela encostar em produção.

Acompanha a *Proposta técnica do diretório de parceiros da Helppi*. A proposta
descreve o acordo; este repositório é o acordo, executável.

## O problema que ele resolve

Hoje o cadastro de um profissional chega até vocês uma única vez, quando ele
entra, e a partir dali os dois lados se afastam. Quando alguém é suspenso ou tem
o cadastro encerrado na Helppi, nada avisa vocês. A conta do lado de vocês segue
aberta até que uma pessoa perceba e peça a correção — por mensagem, por planilha,
sempre depois do fato.

Essa lacuna é um problema de segurança antes de ser um problema operacional:
acesso que sobrevive ao motivo que o justificava.

## O que muda quando isto está rodando

| Hoje | Com o diretório |
|---|---|
| O cadastro chega uma vez, na entrada | O estado é consultado a cada poucos minutos |
| Uma suspensão nunca chega até vocês | A conta é bloqueada dentro do intervalo acordado |
| Não existe chave comum entre os dois lados | Um par estável de identificadores, definido uma vez |
| Alguém percebe e pede a correção | Ninguém intervém; converge sozinho |
| "Essa conta é de qual profissional?" é trabalho manual | A resposta é uma consulta |

## Como funciona, em linguagem simples

A cada poucos minutos o cliente faz uma única pergunta à Helppi: *o que mudou
desde a última vez que perguntei?* A Helppi responde com os profissionais cujo
estado se moveu — alguém entrou, alguém foi suspenso, alguém voltou, alguém teve
o cadastro encerrado. O cliente aplica cada resposta às contas de vocês: cria,
bloqueia, desbloqueia.

Na primeira vez que vê um profissional, ele cria a conta do lado de vocês e
depois **grava o identificador de vocês de volta no registro da Helppi**. Esse
passo é o que cria a chave comum. Dali em diante, as duas empresas apontam para
a mesma pessoa com o mesmo par de identificadores, e ninguém mais precisa
corresponder por nome ou e-mail — o que importa, porque nome e e-mail mudam e são
justamente o dado que nenhuma das duas empresas deveria ficar movimentando.

Uma vez por dia ele faz uma passagem completa, compara tudo com tudo e reporta
qualquer divergência. Ele reporta; não age. Um profissional ausente do diretório
nunca é tratado como ordem para apagar nada.

Três propriedades merecem ser conhecidas, porque são o que torna seguro deixar
isso rodando sem supervisão:

- **Não consegue perder uma atualização.** Se um ciclo falha no meio, ele não
  registra progresso — o ciclo seguinte simplesmente refaz o mesmo trabalho.
- **Repetir não custa nada.** Aplicar a mesma resposta duas vezes não muda nada,
  então tentar de novo é sempre seguro.
- **Nunca adivinha.** Um registro que chega incompleto é pulado e reportado, em
  vez de interpretado — porque interpretar "não sei" como "bloqueado" tiraria o
  acesso de todo mundo de uma vez.

## O que o time de vocês precisa fazer

Três coisas. Todo o resto está neste repositório.

**1 · Ligar ao banco de vocês.** Vocês implementam uma interface — seis métodos:
buscar, criar, atualizar, listar e ler/gravar uma data. É o único código que esta
integração realmente exige que vocês escrevam. Se rodam PostgreSQL, nem isso:
[`store/postgres`](store/postgres) é uma implementação pronta para usar como
está.

**2 · Subir um processo.** O worker está incluído, com métricas e verificação de
saúde. Roda continuamente ou uma vez por execução agendada — o que se encaixar no
jeito de vocês publicarem.

**3 · Devolver o identificador.** Quando criarem uma conta, gravem o id dela de
volta no registro da Helppi. Uma chamada, uma vez por profissional.

Depois é rodar o comando de conformidade contra o ambiente de teste da Helppi.
Ele imprime uma linha de aprovado/reprovado por requisito, e esse relatório **é**
o critério de aceite da Fase 1 — não é opinião, não é reunião.

## O que já vem pronto

| | |
|---|---|
| **Um diretório falso da Helppi** | Desenvolvam e testem offline, sem sandbox e sem credencial. Ele também simula as falhas — limite de requisições, conflito, registro malformado — para vocês verem como o lado de vocês se comporta antes de isso importar. |
| **Um comando de conformidade** | 14 verificações nas duas fases, cada uma nomeando o requisito que defende. Sai com código de erro, então serve de portão no pipeline de vocês. |
| **Uma suíte de contrato** | Apontem para a implementação de banco de vocês e ela confere as regras que a assinatura dos métodos não consegue expressar — inclusive que oito workers simultâneos criam exatamente uma conta, não oito. |
| **Um worker pronto** | Métricas, verificação de saúde, logs estruturados, um modo de simulação que não escreve nada, e uma trava que recusa rodar numa configuração capaz de corromper dados. |
| **Um store PostgreSQL** | Em módulo próprio, para quem não quiser não pagar a dependência. |

## O que isto não faz

- **Não autentica ninguém.** Login único é assunto separado, descrito na
  proposta. Este cliente trata apenas de quem existe e quem pode operar.
- **Não recebe dado pessoal além do mínimo.** Sem e-mail real, sem nome completo,
  sem telefone nem documento. O que atravessa é um identificador opaco, um alias,
  um nome abreviado e um status.
- **Não apaga nada por conta própria.** Ausência no diretório é reportada como
  divergência, nunca executada.
- **Não escreve na Helppi**, com a exceção desse único identificador.

## Perguntas que costumam aparecer

**Precisamos usar Go?**
Não. Se a stack de vocês é outra, este repositório continua útil como
especificação executável: o comportamento está documentado, os casos de falha
estão nomeados, e as fixtures em `testdata/` são os mesmos bytes contra os quais
os dois lados podem testar. O comando de conformidade roda contra qualquer
implementação, em qualquer linguagem, porque só fala HTTP.

**E se uma sincronização falhar?**
Nada quebra. O cliente não registrou progresso, então o ciclo seguinte refaz o
trabalho. Um ciclo que falha é um ciclo atrasado, não um ciclo perdido.

**Em quanto tempo um bloqueio faz efeito?**
No intervalo que as duas empresas acordarem. A proposta sugere cinco minutos; o
número é configuração, não reescrita.

**E se já tivermos contas dessas pessoas?**
A primeira passagem completa encontra as contas pelo identificador do diretório e
as adota. Não cria duplicadas — e existe teste que comprova.

**Como sabemos que terminamos?**
Rodem o `conformance` contra o ambiente de teste da Helppi. Catorze verificações,
cada uma ligada a uma seção da proposta. Quando todas passarem, a Fase 1 está
concluída.

---

# Parte 2 · Referência de engenharia

## Início rápido

```bash
go test ./... -race        # 45 testes, sem rede
make ci                    # gofmt + vet + testes
```

```go
client, err := scim.New(scim.Options{BaseURL: url, Token: token})
if err != nil {
    return err
}

syncer := directory.New(client, meuStore, directory.Options{})

stats, err := syncer.Incremental(ctx)   // um ciclo
```

Depois provem que o store cumpre o contrato:

```go
func TestMeuStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) store.Store { return novoStoreDeTeste(t) })
}
```

## Comecem por uma execução seca

```bash
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… make dry-run
```

Não escreve nada — nem no banco de vocês, nem no diretório, nem no checkpoint.

O worker **se recusa** a rodar de verdade contra um diretório usando o store em
memória. Um store vazio faz todo registro parecer novo, então ele criaria contas
do zero e gravaria por cima de todos os `externalId` do diretório. Liguem um
`store.Store` de verdade antes.

## O modelo: reconciliador, não consumidor de eventos

Cada ciclo recalcula o estado desejado a partir do diretório e converge. Não há
ordem a preservar nem evento a perder, então o processo pode ser morto e
reiniciado em qualquer ponto.

```
a cada 5 min ──► ciclo incremental ──► apply(registro) ──► devolve externalId
                        │                                        │
              checkpoint só avança se o ciclo             409 ⇒ alerta uma vez,
              terminou inteiro                            nunca repetição
a cada 24 h ─► varredura completa ──► relatório de divergência
```

## Estrutura

| Pacote | Responsabilidade |
|---|---|
| `scim` | Protocolo: tipos, cliente HTTP, paginação, retry. Não sabe o que é um helpper. |
| `store` | O contrato do lado local, implementação em memória e suíte de contrato. |
| `store/postgres` | Implementação em PostgreSQL, em módulo próprio para o núcleo seguir sem dependências. |
| `directory` | O reconciliador. Não sabe o que é HTTP. |
| `scimtest` | Diretório falso com injeção de falhas. |
| `conformance` | Os critérios de aceite como verificações executáveis. |
| `cmd/directorysyncd` | O worker: dois tickers, logs estruturados, `/metrics`, `/healthz`, `/readyz`. |
| `cmd/conformance` | Roda as verificações contra um diretório real e imprime o relatório. |

## O contrato do store

```go
type Store interface {
    HelpperByDirectoryID(ctx context.Context, directoryID string) (Helpper, error)
    CreateHelpper(ctx context.Context, p NewHelpper) (Helpper, error)
    UpdateHelpper(ctx context.Context, id string, upd HelpperUpdate) error
    EnabledHelppers(ctx context.Context) ([]Helpper, error)

    Checkpoint(ctx context.Context) (time.Time, error)
    SetCheckpoint(ctx context.Context, at time.Time) error
}
```

`Helpper.ID` é string, então UUID, ULID ou bigint cabem. Vejam
[docs/IMPLEMENTING_STORE.md](docs/IMPLEMENTING_STORE.md) — as duas linhas do
schema que sustentam tudo estão lá, e nenhuma delas é Go.

## Nove decisões que valem discussão

1. **`Active` é `*bool`, não `bool`.** Com `bool`, uma resposta truncada ou um
   atributo ausente decodifica para `false` — e o reconciliador desabilita a
   frota inteira. `nil` é recusado, nunca lido como "desabilitado".
2. **O checkpoint só avança quando o ciclo termina inteiro.** Um ciclo parcial
   que avança a marca d'água perde registros de forma permanente e silenciosa: o
   sintoma aparece semanas depois, como uma conta que ninguém bloqueou.
3. **A marca d'água vem de `meta.lastModified`, não do relógio local.** O desvio
   de relógio entre as duas empresas deixa de importar. Um horário absurdamente
   no futuro é recusado em vez de aceito.
4. **Dois minutos de sobreposição** são relidos a cada ciclo, para absorver
   corridas de visibilidade. É seguro porque aplicar um registro é idempotente.
5. **Registro inválido é pulado, não fatal — mas a marca d'água fica atrás
   dele.** Falhar o ciclo congelaria o checkpoint e pararia a frota inteira por
   causa de uma linha ruim; pular sem segurar a marca d'água perderia a correção
   futura dela.
6. **A correspondência é exclusivamente pelo `id` do diretório.** Nunca por
   login, nome ou alias, em nenhuma etapa, nem na carga inicial.
7. **`409` na devolução nunca é repetido, e alerta uma vez por identidade.**
   Significa que o mapeamento local está errado; repetir não corrige, e alertar a
   cada cinco minutos ensina as pessoas a ignorar o alerta.
8. **Ausência no diretório nunca desprovisiona.** A varredura diária reporta a
   divergência e para por aí.
9. **Resposta que não é SCIM é erro, não diretório vazio.** Uma página HTML de
   bloqueio, decodificada com folga, vira "não trabalha mais ninguém aqui".

## Conformidade

```bash
go run ./cmd/conformance -base-url … -token …
```

Catorze verificações — onze da Fase 1, três da Fase 2 — cada uma nomeando o
requisito que defende. `--json` para saída de máquina. Vejam
[docs/CONFORMANCE.md](docs/CONFORMANCE.md).

## Documentação

| Documento | O que responde |
|---|---|
| [docs/INTEGRATION.md](docs/INTEGRATION.md) | O contrato: modelo de identidade, ciclo de vida, matriz de erros e qual teste defende cada promessa. |
| [docs/IMPLEMENTING_STORE.md](docs/IMPLEMENTING_STORE.md) | Como escrever a única interface que é de vocês. |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Métricas, limiares de alerta e runbook por tipo de falha. |
| [docs/CONFORMANCE.md](docs/CONFORMANCE.md) | Cada verificação, e o requisito por trás dela. |

## Requisitos

Go 1.22 ou mais novo para os pacotes do núcleo, que **não têm dependências de
terceiros** — dá para copiar para dentro do repositório de vocês e compilar
offline. O módulo opcional `store/postgres` precisa de Go 1.24, porque a cadeia
de dependências do pgx precisa.

## Fora de escopo

O acesso com um toque não está aqui. É um cliente OpenID Connect comum, cujo
callback mapeia o identificador do diretório para uma sessão. Vejam a proposta.

## Licença

MIT. Veja [LICENSE](LICENSE).
