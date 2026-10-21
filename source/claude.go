package main

// Assistant Claude de l'application de bureau : la page envoie chaque tour de conversation ici,
// le serveur l'envoie à l'API Anthropic avec la clé API enregistrée sur l'ordinateur.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func keyFile() string { return filepath.Join(dataDir, "claude-key") }

func loadKey() string {
	if b, err := os.ReadFile(keyFile()); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b))
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

func handleClaudeKey(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		k := loadKey()
		hint := ""
		if len(k) > 8 {
			hint = k[:7] + "…" + k[len(k)-4:]
		}
		writeJSON(w, map[string]any{"set": k != "", "hint": hint})
	case "POST":
		var body struct{ Key string }
		if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body) != nil || !strings.HasPrefix(strings.TrimSpace(body.Key), "sk-ant-") {
			http.Error(w, "Clé invalide : elle commence par sk-ant-", 400)
			return
		}
		if err := os.WriteFile(keyFile(), []byte(strings.TrimSpace(body.Key)), 0o600); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	case "DELETE":
		os.Remove(keyFile())
		w.WriteHeader(204)
	default:
		http.Error(w, "méthode", 405)
	}
}

func handleClaude(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "méthode", 405)
		return
	}
	key := loadKey()
	if key == "" {
		writeErr(w, 401, "no_key", "Aucune clé API enregistrée.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil || !json.Valid(body) {
		writeErr(w, 400, "invalid", "Requête invalide.")
		return
	}
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(2)}
	if base := os.Getenv("PAUFC_API_BASE"); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	client := anthropic.NewClient(opts...)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	var out []byte
	err = client.Post(ctx, "v1/messages", nil, &out,
		option.WithRequestBody("application/json", body),
		option.WithHeader("anthropic-beta", "server-side-fallback-2026-07-01"))
	if err != nil {
		var apierr *anthropic.Error
		if errors.As(err, &apierr) {
			code := "api_error"
			switch apierr.StatusCode {
			case 401, 403:
				code = "bad_key"
			case 429:
				code = "rate_limited"
			case 400:
				code = "invalid"
			}
			writeErr(w, apierr.StatusCode, code, apierr.Error())
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		writeErr(w, 502, "network", "Connexion à Claude impossible : vérifie internet. ("+err.Error()+")")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}
