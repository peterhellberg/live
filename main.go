package main

import (
	"bytes"
	"flag"
	"fmt"
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
	dir      string
	port     int
	debounce time.Duration
	exclude  string
	open     bool
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

	flags.StringVar(&cfg.dir, "dir", ".", "directory to serve")
	flags.IntVar(&cfg.port, "port", 9222, "port to listen on")
	flags.DurationVar(&cfg.debounce, "debounce", 100*time.Millisecond, "reload debounce duration (e.g. 50ms, 200ms)")
	flags.StringVar(&cfg.exclude, "exclude", ".git,node_modules,.zig-cache", "comma-separated list of path substrings to ignore")
	flags.BoolVar(&cfg.open, "open", true, "automatically open browser")

	return cfg, flags.Parse(args[1:])
}

func run(args []string) error {
	cfg, err := parse(args)
	if err != nil {
		return err
	}

	var (
		root     = newRootFunc(cfg)
		excludes = strings.Split(cfg.exclude, ",")
		addr     = fmt.Sprintf(":%d", cfg.port)
		rawurl   = "http://localhost" + addr
		r        = newReloader()
	)

	if err := watch(cfg.dir, r, cfg.debounce, excludes); err != nil {
		return err
	}

	http.HandleFunc("/__livereload", r.endpoint)
	http.HandleFunc("/", root)

	fmt.Printf("Live %s at %s (debounce=%s)\n", cfg.dir, rawurl, cfg.debounce)

	if cfg.open {
		go openBrowser(rawurl)
	}

	return http.ListenAndServe(addr, nil)
}

func watch(root string, r *reloader, duration time.Duration, excludes []string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := filepath.WalkDir(root,
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}

			if isExcluded(path, excludes) {
				return filepath.SkipDir
			}

			if d.IsDir() {
				watcher.Add(path)
			}

			return nil
		},
	); err != nil {
		return err
	}

	go func() {
		var timer *time.Timer

		trigger := func() {
			if timer != nil {
				timer.Stop()
			}

			timer = time.AfterFunc(duration, func() {
				r.notify()
			})
		}

		for {
			select {
			case ev := <-watcher.Events:
				if isExcluded(ev.Name, excludes) {
					continue
				}

				if ev.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
						if !isExcluded(ev.Name, excludes) {
							watcher.Add(ev.Name)
						}
					}
				}

				trigger()
			case err := <-watcher.Errors:
				fmt.Println("watch error:", err)
			}
		}
	}()

	return nil
}

func newRootFunc(cfg Config) func(http.ResponseWriter, *http.Request) {
	fs := http.FileServer(http.Dir(cfg.dir))

	return func(w http.ResponseWriter, req *http.Request) {
		path := filepath.Join(cfg.dir, req.URL.Path)

		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			index := filepath.Join(path, "index.html")

			if _, err := os.Stat(index); err == nil {
				req.URL.Path = filepath.Join(req.URL.Path, "index.html")
				path = index
			}
		}

		if info, err := os.Stat(path); err == nil &&
			!info.IsDir() && strings.HasSuffix(path, ".html") {
			if data, err := os.ReadFile(path); err == nil {
				w.Header().Set("Content-Type", "text/html")
				w.Write(injectReload(data))

				return
			}
		}

		fs.ServeHTTP(w, req)
	}
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

	if err := cmd.Start(); err != nil {
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

	for range ch {
		fmt.Fprint(w, "data: reload\n\n")
		flusher.Flush()
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
	close(ch)
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
    const es = new EventSource("/__livereload");
    es.onmessage = () => location.reload();
</script>`)

	if bytes.Contains(html, []byte("</body>")) {
		return bytes.Replace(
			html,
			[]byte("</body>"),
			append(snippet, []byte("</body>")...),
			1,
		)
	}

	return append(html, snippet...)
}

func isExcluded(path string, excludes []string) bool {
	for _, ex := range excludes {
		if ex != "" && strings.Contains(path, ex) {
			return true
		}
	}

	return false
}
