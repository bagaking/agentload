package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type trajectoryAccess struct {
	mutationMu            sync.Mutex
	mu                    sync.RWMutex
	enabled               bool
	root, endpoint, token string
	loadError             bool
}
type trajectoryInstance struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	PID      int    `json:"pid"`
}

func trajectoryRoot(history string) string { return resolveHistoryFile(history) + ".trajectory" }
func newTrajectoryAccess(history, endpoint string) *trajectoryAccess {
	a := &trajectoryAccess{root: trajectoryRoot(history), endpoint: strings.TrimRight(endpoint, "/")}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("cannot generate local instance credential")
	}
	a.token = base64.RawURLEncoding.EncodeToString(b)
	data, err := os.ReadFile(filepath.Join(a.root, "access.json"))
	if err == nil {
		var state struct {
			Enabled bool `json:"enabled"`
		}
		if json.Unmarshal(data, &state) != nil {
			a.loadError = true
		} else {
			a.enabled = state.Enabled
		}
	} else if !os.IsNotExist(err) {
		a.loadError = true
	}
	return a
}
func (a *trajectoryAccess) isEnabled() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}
func privateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (a *trajectoryAccess) setEnabled(enabled bool) error {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := privateJSON(filepath.Join(a.root, "access.json"), struct {
		Enabled bool `json:"enabled"`
	}{enabled}); err != nil {
		return err
	}
	a.enabled = enabled
	a.loadError = false
	return nil
}
func (a *trajectoryAccess) publishInstance() error {
	return privateJSON(filepath.Join(a.root, "instance.json"), trajectoryInstance{Endpoint: a.endpoint, Token: a.token, PID: os.Getpid()})
}
func (a *trajectoryAccess) removeInstance() {
	path := filepath.Join(a.root, "instance.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var instance trajectoryInstance
	if json.Unmarshal(data, &instance) == nil && instance.Token == a.token {
		_ = os.Remove(path)
	}
}
func (a *trajectoryAccess) sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || u.Host != r.Host {
			return false
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	return true
}
func (a *trajectoryAccess) authorized(r *http.Request) bool {
	return a.isEnabled() && a.credential(r)
}
func (a *trajectoryAccess) credential(r *http.Request) bool {
	if !a.sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) == 1
}
func (a *trayApp) handleTrajectoryAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	access := a.trajectoryAccess
	if access == nil {
		http.Error(w, "trajectory unavailable", 503)
		return
	}
	if !access.sameOrigin(r) || r.Header.Get("X-AgentLoad-Local") != "1" {
		http.Error(w, "local UI request required", 403)
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !access.credential(r) {
			http.Error(w, "local instance credential required", 403)
			return
		}
		var state struct {
			Enabled bool `json:"enabled"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&state) != nil {
			http.Error(w, "invalid access preference", 400)
			return
		}
		if err := access.setEnabled(state.Enabled); err != nil {
			http.Error(w, "cannot save local preference", 500)
			return
		}
		if !state.Enabled && a.trajectory != nil {
			if err := a.trajectory.Reset(); err != nil {
				http.Error(w, "index cleanup failed", 500)
				return
			}
		}
		a.notifyArchive()
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	access.mu.RLock()
	loadError := access.loadError
	access.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(struct {
		Enabled       bool `json:"enabled"`
		PreferenceGap bool `json:"preference_gap"`
		Authorized    bool `json:"authorized"`
	}{access.isEnabled(), loadError, access.credential(r)})
}
