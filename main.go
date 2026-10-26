package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Config struct {
	root   string
	addr   string
	wait   time.Duration
	ignore string
	open   bool
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func parse(args []string) (Config, error) {
	var cfg Config

	flags := flag.NewFlagSet(args[0], flag.ExitOnError)

	flags.StringVar(&cfg.root, "root", ".", "directory to serve")
	flags.StringVar(&cfg.addr, "addr", "0.0.0.0:9222", "addr to listen on")
	flags.DurationVar(&cfg.wait, "wait", 100*time.Millisecond, "reload wait duration (e.g. 50ms, 200ms)")
	flags.StringVar(&cfg.ignore, "ignore", ".git,.zig-cache,node_modules", "comma-separated list of path substrings to ignore")
	flags.BoolVar(&cfg.open, "open", true, "automatically open browser")

	return cfg, flags.Parse(args[1:])
}

func run(args []string) error {
	cfg, err := parse(args)
	if err != nil {
		return err
	}

	var (
		ignored = strings.Split(cfg.ignore, ",")
		r       = newReloader()
	)

	if err := watch(cfg.root, r, cfg.wait, ignored); err != nil {
		return err
	}

	http.HandleFunc("/__livereload", r.endpoint)
	http.HandleFunc("/", newRootFunc(cfg))

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return err
	}

	rawurl := browserURL(ln.Addr())

	fmt.Printf("⟳ %s %q at %s\n", cfg.wait, cfg.root, rawurl)

	if cfg.open {
		go openBrowser(rawurl)
	}

	return http.Serve(ln, nil)
}

// browserURL returns a URL that a local browser can open,
// replacing unspecified hosts (e.g. 0.0.0.0 or ::) with localhost.
func browserURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}

	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}

	return "http://" + net.JoinHostPort(host, port)
}

type watchState struct {
	mu      sync.Mutex
	lastMod map[string]time.Time
	timer   *time.Timer
}

func newWatchState() *watchState {
	return &watchState{
		lastMod: make(map[string]time.Time),
	}
}

func (ws *watchState) trigger(path string, delay time.Duration, r *reloader) {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()

	if err != nil {
		// Removed or renamed away, reload so the page reflects it.
		delete(ws.lastMod, path)
	} else {
		mod := info.ModTime()

		if last, ok := ws.lastMod[path]; ok && !mod.After(last) {
			return
		}

		ws.lastMod[path] = mod
	}

	if ws.timer != nil {
		ws.timer.Stop()
	}

	ws.timer = time.AfterFunc(delay, r.notify)
}

func watchDirRecursive(w *fsnotify.Watcher, dir string, ignored func(string) bool) {
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if ignored(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if d.IsDir() {
			_ = w.Add(path)
		}

		return nil
	})
}

func watch(root string, r *reloader, delay time.Duration, ignore []string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	// Match ignore substrings against paths relative to root, so that
	// a root such as ~/node_modules_demo/site is not ignored entirely.
	ignored := func(path string) bool {
		if rel, err := filepath.Rel(root, path); err == nil {
			path = rel
		}

		return isIgnored(path, ignore)
	}

	watchDirRecursive(watcher, root, ignored)

	ws := newWatchState()

	go func() {
		for {
			select {
			case ev := <-watcher.Events:
				if ignored(ev.Name) {
					continue
				}

				if ev.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
						watchDirRecursive(watcher, ev.Name, ignored)
					}
				}

				ws.trigger(ev.Name, delay, r)
			case err := <-watcher.Errors:
				fmt.Println("watch error:", err)
			}
		}
	}()

	return nil
}

func newRootFunc(cfg Config) func(http.ResponseWriter, *http.Request) {
	dir := http.Dir(cfg.root)
	fs := http.FileServer(dir)

	return func(w http.ResponseWriter, req *http.Request) {
		name := req.URL.Path

		// Directories without a trailing slash are left to the
		// file server, which redirects so relative URLs resolve.
		if strings.HasSuffix(name, "/") {
			name += "index.html"
		}

		if strings.HasSuffix(name, ".html") {
			if data, err := readFile(dir, name); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(injectReload(data))

				return
			}
		}

		fs.ServeHTTP(w, req)
	}
}

func readFile(dir http.Dir, name string) ([]byte, error) {
	f, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(f)
}

func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Run(); err != nil {
		fmt.Println("failed to open browser:", err)
	}
}

type reloader struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newReloader() *reloader {
	return &reloader{
		clients: make(map[chan struct{}]struct{}),
	}
}

func (r *reloader) endpoint(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)

		return
	}

	ch := r.add()
	defer r.remove(ch)

	flusher.Flush()

	for {
		select {
		case <-req.Context().Done():
			return
		case <-ch:
			fmt.Fprint(w, "data: reload\n\n")
			flusher.Flush()
		}
	}
}

func (r *reloader) add() chan struct{} {
	ch := make(chan struct{}, 1)

	r.mu.Lock()
	r.clients[ch] = struct{}{}
	r.mu.Unlock()

	return ch
}

func (r *reloader) remove(ch chan struct{}) {
	r.mu.Lock()
	delete(r.clients, ch)
	r.mu.Unlock()
}

func (r *reloader) notify() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for ch := range r.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func injectReload(html []byte) []byte {
	snippet := []byte(`<script>
if(window.fetch){const o=window.fetch;window.fetch=(...a)=>{if(typeof a[0]==="string"&&a[0].endsWith(".wasm"))a[0]=a[0].split("?")[0]+"?_="+Date.now();return o(...a)}};const e=new EventSource("/__livereload");e.onmessage=(ev)=>{if(ev.data!=="reload")return;const n=Date.now();document.querySelectorAll("script[src], link[rel=stylesheet]").forEach(el=>{if(el.src)el.src=el.src.split("?")[0]+"?_="+n;if(el.href)el.href=el.href.split("?")[0]+"?_="+n}),location.reload()};</script>`)

	if bytes.Contains(html, []byte("<head>")) {
		return bytes.Replace(html, []byte("<head>"), append([]byte("<head>"), snippet...), 1)
	}

	return append(html, snippet...)
}

func isIgnored(path string, ignored []string) bool {
	for _, ex := range ignored {
		if ex != "" && strings.Contains(path, ex) {
			return true
		}
	}

	return false
}
