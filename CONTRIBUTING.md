# Contributing

This is a reference client: small, dependency-free and read carefully by people
deciding whether to trust an integration. Changes are welcome; changes that keep
it small are more welcome.

## Before opening a pull request

```bash
make ci    # gofmt, vet, tests with -race
```

## House rules

- **No third-party dependencies in the core packages.** A partner must be able
  to vendor `scim`, `directory`, `store` and `scimtest` and build them offline,
  with nothing to clear through a dependency review. Anything that needs a
  driver belongs in its own module under `store/`.
- **Every behavioral rule gets a test that fails without it.** The test suite
  doubles as the acceptance criteria for the integration; a rule with no test is
  a rule nobody can verify.
- **Say why, not what, in comments.** The code says what it does. Comments are
  for the decision behind it — especially where the obvious implementation is
  wrong, such as `Active` being a pointer.
- **Name tests after the behavior they pin**, not the function they call:
  `TestMalformedRecordIsSkippedNotFatal`, not `TestApply3`.
- **Português no README, inglês no código.** O README acompanha a proposta e é
  lido por produto e engenharia do parceiro, em português. Código, comentários,
  godoc, CHANGELOG e os guias em `docs/` ficam em inglês: comentário é lido
  dentro da IDE, e é a convenção do ecossistema Go.
- **Changes to the contract go in `docs/INTEGRATION.md` in the same PR.** If the
  two disagree, the document is what the partner implemented against.
