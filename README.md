# live 🔄

Live reloading of static HTML, similar to the 
[live-server](https://www.npmjs.com/package/live-server)
NPM package, but implemented in Go.

## Installation

Requires you to have [Go](https://go.dev/) installed.

```sh
go install github.com/peterhellberg/live@latest
```

> [!Tip]
> You can also use `go run github.com/peterhellberg/live@latest`

## Usage

```console
$ live -h
Usage of live:
  -addr string
        addr to listen on (default "0.0.0.0:9222")
  -ignore string
        comma-separated list of path substrings to ignore (default ".git,.zig-cache,node_modules")
  -open
        automatically open browser (default true)
  -root string
        directory to serve (default ".")
  -wait duration
        reload wait duration (e.g. 50ms, 200ms) (default 100ms)
```
