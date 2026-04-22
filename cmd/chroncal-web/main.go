// chroncal-web is a POC HTTP server that exposes the chroncal TUI over the
// browser via a wterm frontend and a PTY-backed WebSocket.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

//go:embed static
var staticFS embed.FS

var resizeRE = regexp.MustCompile(`\x1b\[RESIZE:(\d+);(\d+)\]`)

func main() {
	addr := flag.String("addr", "127.0.0.1:3000", "listen address")
	bin := flag.String("bin", "./chroncal", "path to the chroncal binary to spawn")
	isolated := flag.Bool("isolated", false, "give each WebSocket session its own temp CHRONCAL_DB (for e2e tests)")
	flag.Parse()

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("embed fs: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/wterm.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/wasm")
		http.ServeFileFS(w, r, sub, "wterm.wasm")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFileFS(w, r, sub, "index.html")
	})
	mux.HandleFunc("/api/terminal", func(w http.ResponseWriter, r *http.Request) {
		serveTerminal(w, r, *bin, *isolated)
	})

	log.Printf("chroncal-web listening on http://%s (bin=%s isolated=%t)", *addr, *bin, *isolated)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func serveTerminal(w http.ResponseWriter, r *http.Request, bin string, isolated bool) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("ws accept: %v", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	env := append(os.Environ(), "TERM=xterm-256color")
	if isolated {
		dir, err := os.MkdirTemp("", "chroncal-web-*")
		if err != nil {
			log.Printf("mktemp: %v", err)
			return
		}
		defer os.RemoveAll(dir)
		env = append(env, "CHRONCAL_DB="+dir+"/chroncal.db")
	}

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = env

	f, err := pty.Start(cmd)
	if err != nil {
		log.Printf("pty start: %v", err)
		_ = ws.Write(ctx, websocket.MessageText, []byte("\r\n\x1b[31mfailed to spawn: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	defer func() {
		_ = f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	var writeMu sync.Mutex
	sendText := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		return ws.Write(wctx, websocket.MessageText, data)
	}

	// PTY → WebSocket
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if werr := sendText(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				// EIO on the pty master is Linux's signal that the slave
				// side (chroncal) has exited — same meaning as EOF on
				// other platforms. Both are normal session termination.
				if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) {
					log.Printf("pty read: %v", err)
				}
				return
			}
		}
	}()

	// WebSocket → PTY
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if m := resizeRE.FindSubmatch(data); m != nil {
			cols, _ := strconv.Atoi(string(m[1]))
			rows, _ := strconv.Atoi(string(m[2]))
			if cols > 0 && rows > 0 {
				_ = pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
			}
			continue
		}
		if _, err := f.Write(data); err != nil {
			return
		}
	}
}
