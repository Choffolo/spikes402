# Contributing

Issues and pull requests are welcome. Please write in English, keep changes
focused, and do not include secrets, seed phrases, or real node passwords.

## Tests

```sh
go test -race ./...
```

For the browser wallet:

```sh
cd extension
npm install
npm test
npm run lint
```

`gofmt` should be clean on Go files. Do not commit `extension/node_modules/`,
`extension/dist/`, or `extension/spikes402-extension.zip`.

## Security

Do not file public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).
