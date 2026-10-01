package main

// server.go — HTTP-сервер: отдаёт интерфейс и JSON-API Soundpad.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type server struct {
	store  *store
	player *player
	log    *log.Logger
	start  time.Time
}

func newServer(st *store, p *player, l *log.Logger) *server {
	return &server{store: st, player: p, log: l, start: time.Now()}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/app.js", s.handleJS)
	mux.HandleFunc("/app.css", s.handleCSS)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/play", s.handlePlay)
	mux.HandleFunc("/api/stop", s.handleStop)
	mux.HandleFunc("/api/audio", s.handleAudio)
	mux.HandleFunc("/api/volume", s.handleVolume)
	mux.HandleFunc("/api/padvol", s.handlePadVol)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/sound", s.handleSound)
	mux.HandleFunc("/api/move", s.handleMove)
	mux.HandleFunc("/api/page", s.handlePage)
	return s.withLogging(mux)
}

func (s *server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Printf("%s %s", r.Method, r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

// ── отдача интерфейса ──────────────────────────────────────────────────────

func (s *server) serveAsset(w http.ResponseWriter, r *http.Request, name, ctype string) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := io.WriteString(w, name); err != nil {
		return
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, indexHTML)
}

func (s *server) handleJS(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, r, appJS, "application/javascript; charset=utf-8")
}
func (s *server) handleCSS(w http.ResponseWriter, r *http.Request) {
	s.serveAsset(w, r, appCSS, "text/css; charset=utf-8")
}

// ── JSON-ответы ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

// ── состояние ──────────────────────────────────────────────────────────────

type stateOut struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Hue   int     `json:"hue"`
	Dur   float64 `json:"dur"`
	Built bool    `json:"built"`
	Gain  float64 `json:"gain,omitempty"`
}

func (s *server) statePayload() map[string]any {
	pages, st := s.store.snapshot()
	out := make([]map[string]any, 0, len(pages))
	for _, p := range pages {
		sounds := make([]any, maxSoundsPage)
		for i, snd := range p.Sounds {
			if snd.ID == "" {
				continue
			}
			sounds[i] = stateOut{
				ID:    snd.ID,
				Name:  snd.Name,
				Hue:   snd.Hue,
				Dur:   snd.Dur,
				Built: snd.Built,
				Gain:  snd.Gain,
			}
		}
		out = append(out, map[string]any{
			"name":   p.Name,
			"title":  p.Title,
			"sounds": sounds,
		})
	}
	mode := "none"
	switch s.player.currentMode() {
	case modeFFmpeg:
		mode = "ffmpeg"
	case modeFFplay:
		mode = "ffplay"
	}
	return map[string]any{
		"pages":     out,
		"volume":    st.Volume,
		"padVol":    st.PadVol,
		"mode":      mode,
		"wasm":      false,
		"dir":       s.store.dir,
		"upSeconds": time.Since(s.start).Seconds(),
	}
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.statePayload())
}

// ── воспроизведение ────────────────────────────────────────────────────────

type playReq struct {
	ID   string  `json:"id"`
	Mode string  `json:"mode"`
	Gain float64 `json:"gain"`
}

func (s *server) handlePlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("нужен POST"))
		return
	}
	var req playReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("некорректный запрос: %w", err))
		return
	}
	path, err := s.store.soundPath(req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "toggle"
	}
	gain := req.Gain
	if gain == 0 {
		gain = s.store.gainOf(req.ID)
	}

	switch s.player.currentMode() {
	case modeFFmpeg:
		// Готовим звук и отдаём его браузеру: он и воспроизводит.
		data, ctype, err := s.player.clipForBrowser(path, gain)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Soundpad-Sound", req.ID)
		if _, err := w.Write(data); err != nil {
			s.log.Printf("не удалось отдать звук %s: %v", req.ID, err)
		}
	case modeFFplay:
		playing, _, err := s.player.play(path, req.ID, mode, gain)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "playing": playing, "server": true})
	default:
		writeErr(w, http.StatusServiceUnavailable,
			errors.New("ffmpeg не найден: воспроизведение на сервере выключено, звук будет играть браузер"))
	}
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("нужен POST"))
		return
	}
	s.player.stopAll()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAudio отдаёт файл звука как есть — используется браузером, когда
// ffmpeg недоступен (играем WAV напрямую).
func (s *server) handleAudio(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	path, err := s.store.soundPath(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

// ── громкость ──────────────────────────────────────────────────────────────

func (s *server) handleVolume(w http.ResponseWriter, r *http.Request) {
	s.handleFloatSetting(w, r, func(v float64) error { return s.store.setVolume(v) })
}

func (s *server) handlePadVol(w http.ResponseWriter, r *http.Request) {
	s.handleFloatSetting(w, r, func(v float64) error { return s.store.setPadVol(v) })
}

func (s *server) handleFloatSetting(w http.ResponseWriter, r *http.Request, set func(float64) error) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("нужен POST"))
		return
	}
	var req struct {
		Value *float64 `json:"value"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Value == nil {
		writeErr(w, http.StatusBadRequest, errors.New("ожидается поле value"))
		return
	}
	if err := set(*req.Value); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── загрузка звуков ────────────────────────────────────────────────────────

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("нужен POST"))
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("не удалось разобрать форму: %w", err))
		return
	}
	pageName := r.FormValue("page")
	slot, err := strconv.Atoi(r.FormValue("slot"))
	if err != nil || slot < 1 || slot > maxSoundsPage {
		writeErr(w, http.StatusBadRequest, errors.New("некорректный номер пэда"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("файл не получен"))
		return
	}
	defer file.Close()

	limited := io.LimitReader(file, maxUploadSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if int64(len(data)) > maxUploadSize {
		writeErr(w, http.StatusRequestEntityTooLarge,
			fmt.Errorf("файл больше %d МБ", maxUploadSize>>20))
		return
	}
	if len(data) < 64 {
		writeErr(w, http.StatusBadRequest, errors.New("файл слишком маленький"))
		return
	}

	// Проверяем, что это звук, который умеет читать ffmpeg, и заодно
	// узнаём длительность (для WAV её даст и собственный парсер).
	dur := 0.0
	if s.player.currentMode() == modeFFmpeg {
		tmp, err := writeTemp(data)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		probed, perr := s.player.probeFile(tmp)
		removeQuiet(tmp)
		if perr != nil {
			writeErr(w, http.StatusBadRequest,
				fmt.Errorf("ffmpeg не смог прочитать файл как звук: %s", perr))
			return
		}
		dur = probed
	}

	if _, err := s.store.saveSound(pageName, slot, header.Filename, data, dur); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.log.Printf("загружен звук %q → %s (пэд %d)", header.Filename, pageName, slot)
	writeJSON(w, http.StatusOK, s.statePayload())
}

// ── операции над звуками ───────────────────────────────────────────────────

func (s *server) handleSound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("нужен POST"))
		return
	}
	var req struct {
		Action string   `json:"action"`
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Gain   *float64 `json:"gain"`
		Slot   int      `json:"slot"`
		Page   string   `json:"page"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("некорректный запрос"))
		return
	}
	var err error
	switch req.Action {
	case "rename":
		err = s.store.renameSound(req.ID, strings.TrimSpace(req.Name))
	case "gain":
		if req.Gain == nil {
			err = errors.New("нужно поле gain")
		} else {
			err = s.store.setGain(req.ID, *req.Gain)
		}
	case "delete":
		s.player.stopAll()
		err = s.store.deleteSound(req.ID)
	case "move":
		err = s.store.moveSound(req.ID, req.Page, req.Slot)
	default:
		err = fmt.Errorf("неизвестное действие %q", req.Action)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload())
}

func (s *server) handleMove(w http.ResponseWriter, r *http.Request) { s.handleSound(w, r) }

// ── страницы ───────────────────────────────────────────────────────────────

func (s *server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("нужен POST"))
		return
	}
	var req struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Title  string `json:"title"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("некорректный запрос"))
		return
	}
	var err error
	switch req.Action {
	case "add":
		_, err = s.store.addPage()
	case "title":
		err = s.store.setPageTitle(req.Name, strings.TrimSpace(req.Title))
	case "delete":
		s.player.stopAll()
		err = s.store.deletePage(req.Name)
	default:
		err = fmt.Errorf("неизвестное действие %q", req.Action)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.statePayload())
}
