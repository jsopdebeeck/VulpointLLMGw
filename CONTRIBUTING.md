# Contributing to LLMGW

Issues and pull requests are welcome, in English or French.

## Never post real personal data

Examples, test cases and bug reports must use **invented** names, emails, phone numbers, national numbers and IBANs (`Claire Dubois`, `claire.dubois@acme.be`, `+32 470 12 34 56`, documentation IP ranges such as `203.0.113.0/24`). This applies to logs and Debug archives too: they contain original values in clear text.

## Development

```bash
go build -o llmgw .            # Go 1.25 or later, no CGO needed
go vet ./... && go test ./...  # end-to-end tests with fake LLMs
go test -bench Anon            # anonymisation benchmarks
examples/demo/run.sh           # manual check, no provider key needed
integrations/hermes/tests/run_tests.sh
```

- A detection fix comes with a regression test in `anon_test.go`: the text that was missed (or wrongly caught) and the expected output.
- Any change to the dashboard or the chat updates the three languages (EN, FR, NL) in `i18n.js`.
- The gateway stays a single binary without external services: new dependencies need a good reason.
- A user-visible change bumps `Version` in `main.go` and adds an entry to `RELEASE_NOTES.md`.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
