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
  -debounce duration
        reload debounce duration (e.g. 50ms, 200ms) (default 100ms)
  -dir string
        directory to serve (default ".")
  -exclude string
        comma-separated list of path substrings to ignore (default ".git,node_modules,.zig-cache")
  -open
        automatically open browser (default true)
  -port int
        port to listen on (default 9222)
```
