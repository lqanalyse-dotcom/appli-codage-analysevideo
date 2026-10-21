package main

// Mises à jour automatiques de la page : au lancement, l'appli lit version.json à l'adresse de publication ;
// si la version est plus récente, elle télécharge index.html et la garde dans le dossier des données.
// Hors connexion, la dernière version téléchargée (ou celle intégrée au programme) est utilisée.
import (
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Adresse de publication des mises à jour (vide = mises à jour désactivées)
var updateBase = "https://raw.githubusercontent.com/lqanalyse-dotcom/appli-codage-analysevideo/main/app"

type pageVersion struct {
	Version int `json:"version"`
}

func embeddedVersion() int {
	var v pageVersion
	if b, err := fs.ReadFile(webFS, "web/version.json"); err == nil {
		json.Unmarshal(b, &v)
	}
	return v.Version
}

func cacheDir() string { return filepath.Join(dataDir, "maj") }

func cachedVersion() int {
	var v pageVersion
	if b, err := os.ReadFile(filepath.Join(cacheDir(), "version.json")); err == nil {
		json.Unmarshal(b, &v)
	}
	if _, err := os.Stat(filepath.Join(cacheDir(), "index.html")); err != nil {
		return 0
	}
	return v.Version
}

// Sert la page la plus récente entre celle intégrée et celle téléchargée
func servePage(static http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.URL.Path == "/" || r.URL.Path == "/index.html") && cachedVersion() > embeddedVersion() {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(cacheDir(), "index.html"))
			return
		}
		static.ServeHTTP(w, r)
	})
}

func checkUpdate() {
	base := strings.TrimRight(os.Getenv("PAUFC_UPDATE_BASE"), "/")
	if base == "" {
		base = strings.TrimRight(updateBase, "/")
	}
	if base == "" {
		return
	}
	client := &http.Client{Timeout: 20 * time.Second}
	get := func(name string) ([]byte, error) {
		resp, err := client.Get(base + "/" + name + "?t=" + time.Now().Format("20060102150405"))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, os.ErrNotExist
		}
		return io.ReadAll(io.LimitReader(resp.Body, 30<<20))
	}
	vb, err := get("version.json")
	var v pageVersion
	if err != nil || json.Unmarshal(vb, &v) != nil {
		log.Println("mise à jour : version introuvable", err)
		return
	}
	cur := embeddedVersion()
	if c := cachedVersion(); c > cur {
		cur = c
	}
	if v.Version <= cur {
		return
	}
	page, err := get("index.html")
	if err != nil || !strings.Contains(string(page), "Pau FC Analyse") || !strings.Contains(string(page), "/shim.js") {
		log.Println("mise à jour : page invalide", err)
		return
	}
	os.MkdirAll(cacheDir(), 0o755)
	if atomicWrite(filepath.Join(cacheDir(), "index.html"), page) == nil {
		atomicWrite(filepath.Join(cacheDir(), "version.json"), vb)
		updated.Store(true)
		log.Println("mise à jour téléchargée : version", v.Version)
	}
}
