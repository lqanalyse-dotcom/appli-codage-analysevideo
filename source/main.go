// Pau FC Analyse : application de bureau.
// Un petit serveur local sert le dashboard et stocke matchs, documents et exports
// sur l'ordinateur ; l'interface s'ouvre dans une fenêtre dédiée (Edge/Chrome en mode application).
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
var webFS embed.FS

const (
	addr     = "127.0.0.1:47652"
	appName  = "PauFC-Analyse"
	maxBlob  = 200 << 20
	maxFiles = 2000
	maxBytes = 20 << 30
)

var (
	dataDir   string
	mu        sync.Mutex
	lastPing  atomic.Int64
	updated   atomic.Bool
	pathOK    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}(/[A-Za-z0-9_-]{1,80})*$`)
	idOK      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	nameClean = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
)

func main() {
	base, err := os.UserConfigDir()
	if err != nil {
		base, _ = os.UserHomeDir()
	}
	dataDir = filepath.Join(base, appName)
	must(os.MkdirAll(filepath.Join(dataDir, "db"), 0o755))
	must(os.MkdirAll(filepath.Join(dataDir, "blobs"), 0o755))
	if f, err := os.OpenFile(filepath.Join(dataDir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		log.SetOutput(f)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Déjà lancée : on rouvre simplement une fenêtre
		openWindow()
		return
	}

	mux := http.NewServeMux()
	web, _ := fs.Sub(webFS, "web")
	mux.Handle("/", servePage(noCache(http.FileServer(http.FS(web)))))
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]any{"ready": updated.Load()}) })
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) { lastPing.Store(time.Now().Unix()); w.WriteHeader(204) })
	mux.HandleFunc("/api/doc", handleDoc)
	mux.HandleFunc("/api/col", handleCol)
	mux.HandleFunc("/api/blob", handleBlobUpload)
	mux.HandleFunc("/api/blob/", handleBlobDelete)
	mux.HandleFunc("/api/blobs", handleBlobList)
	mux.HandleFunc("/_blob/", handleBlobGet)
	mux.HandleFunc("/api/save", handleSave)
	mux.HandleFunc("/api/claude", handleClaude)
	mux.HandleFunc("/api/claude/key", handleClaudeKey)

	// Fermeture automatique quand la fenêtre n'envoie plus de signal de vie
	lastPing.Store(time.Now().Unix() + 60)
	go func() {
		for range time.Tick(5 * time.Second) {
			if time.Now().Unix()-lastPing.Load() > 45 && os.Getenv("PAUFC_KEEP") == "" {
				log.Println("fenêtre fermée, arrêt")
				os.Exit(0)
			}
		}
	}()

	if os.Getenv("PAUFC_NO_WINDOW") == "" {
		go openWindow()
	}
	go checkUpdate()
	log.Println("serveur sur", addr, "données dans", dataDir)
	log.Fatal(http.Serve(ln, localOnly(mux)))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// Refuse les requêtes venues d'autres sites (protection des données locales)
func localOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if host != addr && host != "localhost:47652" {
			http.Error(w, "interdit", 403)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+addr && o != "http://localhost:47652" {
			http.Error(w, "interdit", 403)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func atomicWrite(p string, data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Base de données : un fichier JSON par document, rangé comme son chemin (matches/abc.json)
func docFile(path string) (string, bool) {
	if !pathOK.MatchString(path) || strings.Count(path, "/")%2 != 1 {
		return "", false
	}
	return filepath.Join(dataDir, "db", filepath.FromSlash(path)+".json"), true
}

func handleDoc(w http.ResponseWriter, r *http.Request) {
	f, ok := docFile(r.URL.Query().Get("path"))
	if !ok {
		http.Error(w, "chemin invalide", 400)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	switch r.Method {
	case "GET":
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, map[string]any{"exists": false, "data": nil})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"exists": true, "data": json.RawMessage(b)})
	case "PUT":
		b, err := io.ReadAll(io.LimitReader(r.Body, 50<<20))
		if err != nil || !json.Valid(b) {
			http.Error(w, "JSON invalide", 400)
			return
		}
		must2(w, os.MkdirAll(filepath.Dir(f), 0o755))
		if err := atomicWrite(f, b); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	case "DELETE":
		os.Remove(f)
		w.WriteHeader(204)
	default:
		http.Error(w, "méthode", 405)
	}
}

func must2(w http.ResponseWriter, err error) {
	if err != nil {
		log.Println(err)
	}
}

func handleCol(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if !pathOK.MatchString(p) || strings.Count(p, "/")%2 != 0 {
		http.Error(w, "chemin invalide", 400)
		return
	}
	dir := filepath.Join(dataDir, "db", filepath.FromSlash(p))
	mu.Lock()
	defer mu.Unlock()
	out := []map[string]any{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil || !json.Valid(b) {
			continue
		}
		out = append(out, map[string]any{"id": strings.TrimSuffix(n, ".json"), "data": json.RawMessage(b)})
	}
	writeJSON(w, out)
}

// Fichiers stockés : blobs/<id> + blobs/<id>.meta (type et date)
type blobMeta struct {
	ContentType string `json:"contentType"`
	CreatedAt   int64  `json:"createdAt"`
	Size        int64  `json:"sizeBytes"`
}

func blobInfo(id string, m blobMeta) map[string]any {
	return map[string]any{"id": id, "url": "/_blob/" + id, "contentType": m.ContentType, "sizeBytes": m.Size,
		"createdAt": time.UnixMilli(m.CreatedAt).UTC().Format(time.RFC3339)}
}

func handleBlobUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "méthode", 405)
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBlob+1))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if len(b) > maxBlob {
		http.Error(w, "Fichier trop volumineux (200 Mo max)", 413)
		return
	}
	rb := make([]byte, 16)
	rand.Read(rb)
	id := hex.EncodeToString(rb)
	ct := r.Header.Get("X-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	m := blobMeta{ContentType: ct, CreatedAt: time.Now().UnixMilli(), Size: int64(len(b))}
	dir := filepath.Join(dataDir, "blobs")
	if err := atomicWrite(filepath.Join(dir, id), b); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	mb, _ := json.Marshal(m)
	atomicWrite(filepath.Join(dir, id+".meta"), mb)
	writeJSON(w, blobInfo(id, m))
}

func readMeta(id string) (blobMeta, bool) {
	var m blobMeta
	b, err := os.ReadFile(filepath.Join(dataDir, "blobs", id+".meta"))
	if err != nil || json.Unmarshal(b, &m) != nil {
		return m, false
	}
	return m, true
}

func handleBlobGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/_blob/")
	m, ok := readMeta(id)
	if !idOK.MatchString(id) || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", m.ContentType)
	http.ServeFile(w, r, filepath.Join(dataDir, "blobs", id))
}

func handleBlobDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/blob/")
	if r.Method != "DELETE" || !idOK.MatchString(id) {
		http.Error(w, "requête invalide", 400)
		return
	}
	os.Remove(filepath.Join(dataDir, "blobs", id))
	os.Remove(filepath.Join(dataDir, "blobs", id+".meta"))
	writeJSON(w, map[string]any{"deleted": true})
}

func handleBlobList(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(filepath.Join(dataDir, "blobs"))
	list := []map[string]any{}
	var total int64
	for _, e := range entries {
		id := e.Name()
		if !idOK.MatchString(id) {
			continue
		}
		if m, ok := readMeta(id); ok {
			list = append(list, blobInfo(id, m))
			total += m.Size
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i]["createdAt"].(string) > list[j]["createdAt"].(string) })
	writeJSON(w, map[string]any{"assets": list, "usage": map[string]any{"files": len(list), "bytes": total, "maxFiles": maxFiles, "maxBytes": maxBytes}})
}

// Exports (PDF, XML, JSON…) : Documents/Pau FC Analyse/Exports, puis ouverture du dossier
func exportDir() string {
	home, _ := os.UserHomeDir()
	docs := filepath.Join(home, "Documents")
	if _, err := os.Stat(docs); err != nil {
		docs = home
	}
	return filepath.Join(docs, "Pau FC Analyse", "Exports")
}

func handleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "méthode", 405)
		return
	}
	name := strings.TrimSpace(nameClean.ReplaceAllString(filepath.Base(r.URL.Query().Get("name")), "_"))
	if name == "" || name == "." || name == ".." {
		name = "export"
	}
	dir := exportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			break
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBlob))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	reveal(p)
	writeJSON(w, map[string]any{"path": p})
}

func reveal(p string) {
	if os.Getenv("PAUFC_NO_WINDOW") != "" {
		return
	}
	switch runtime.GOOS {
	case "windows":
		hide(exec.Command("explorer", "/select,", p)).Start()
	case "darwin":
		exec.Command("open", "-R", p).Start()
	default:
		exec.Command("xdg-open", filepath.Dir(p)).Start()
	}
}

// Fenêtre : navigateur Chromium installé en mode application (sans barre d'adresse)
func openWindow() {
	url := "http://" + addr + "/"
	profile := filepath.Join(dataDir, "fenetre")
	args := []string{"--app=" + url, "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "--window-size=1440,900"}
	for _, b := range browserCandidates() {
		if _, err := os.Stat(b); err == nil {
			if err := hide(exec.Command(b, args...)).Start(); err == nil {
				return
			}
		}
	}
	switch runtime.GOOS {
	case "windows":
		hide(exec.Command("rundll32", "url.dll,FileProtocolHandler", url)).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
	// Navigateur classique : pas de signal de vie fiable à la fermeture d'onglet, on garde une marge
	lastPing.Store(time.Now().Unix() + 120)
}

func browserCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
			root := os.Getenv(env)
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`))
		}
		return out
	case "darwin":
		home, _ := os.UserHomeDir()
		var out []string
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			out = append(out,
				filepath.Join(root, "Google Chrome.app/Contents/MacOS/Google Chrome"),
				filepath.Join(root, "Microsoft Edge.app/Contents/MacOS/Microsoft Edge"),
				filepath.Join(root, "Brave Browser.app/Contents/MacOS/Brave Browser"),
				filepath.Join(root, "Chromium.app/Contents/MacOS/Chromium"))
		}
		return out
	default:
		return []string{"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/microsoft-edge"}
	}
}
