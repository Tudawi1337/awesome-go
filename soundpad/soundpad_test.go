package main

// Тесты Soundpad: генерация демо-звуков, разбор WAV, операции с пэдами
// и HTTP-API.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) (*store, *player) {
	t.Helper()
	logger := log.New(io.Discard, "", 0)
	p := newPlayer(logger)
	st, err := newStore(t.TempDir(), p)
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	return st, p
}

func TestDemoLibraryIsGenerated(t *testing.T) {
	st, _ := testStore(t)

	pages, state := st.snapshot()
	if len(pages) != 3 {
		t.Fatalf("ожидалось 3 страницы, получили %d", len(pages))
	}
	if state.Volume != 100 || state.PadVol != 3 {
		t.Fatalf("настройки по умолчанию неверны: %+v", state)
	}

	first := pages[0]
	if first.Name != "page-001" {
		t.Fatalf("первая страница называется %q", first.Name)
	}
	filled := 0
	for _, snd := range first.Sounds {
		if snd.ID == "" {
			continue
		}
		filled++
		if snd.Dur <= 0 || snd.Dur > 5 {
			t.Errorf("пэд %s: странная длительность %v", snd.ID, snd.Dur)
		}
		if !snd.Built {
			t.Errorf("пэд %s должен считаться встроенным", snd.ID)
		}
	}
	if filled != 16 {
		t.Fatalf("ожидалось 16 демо-звуков, получили %d", filled)
	}
	if first.Sounds[0].Name != "Kick" {
		t.Errorf("первый пэд назван %q, ожидался Kick", first.Sounds[0].Name)
	}

	path, err := st.soundPath("page-001/1")
	if err != nil {
		t.Fatalf("soundPath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл демо-звука не создан: %v", err)
	}

	// Перезапуск не должен ломать метаданные.
	st2, err := newStore(st.dir, newPlayer(log.New(io.Discard, "", 0)))
	if err != nil {
		t.Fatalf("повторный newStore: %v", err)
	}
	pages2, _ := st2.snapshot()
	if pages2[0].Sounds[0].Name != "Kick" {
		t.Errorf("после перезапуска имя пэда потерялось: %q", pages2[0].Sounds[0].Name)
	}
}

func TestWavDurationMatchesEncoder(t *testing.T) {
	tracks := demoTracks()
	if len(tracks) != 16 {
		t.Fatalf("ожидалось 16 встроенных звуков, получили %d", len(tracks))
	}
	for _, tr := range tracks {
		data := renderDemo(tr)
		if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
			t.Fatalf("%s: некорректный WAV-заголовок", tr.Slug)
		}
		path := filepath.Join(t.TempDir(), tr.Slug+".wav")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		dur, err := wavDuration(path)
		if err != nil {
			t.Fatalf("%s: wavDuration: %v", tr.Slug, err)
		}
		if dur <= 0 || dur > 5 {
			t.Errorf("%s: длительность %v выглядит подозрительно", tr.Slug, dur)
		}
	}
}

func TestSoundOperations(t *testing.T) {
	st, _ := testStore(t)

	// загрузка на пустой пэд
	snd, err := st.saveSound("page-002", 3, "Мой звук.wav", renderDemo(demoTracks()[0]), 0)
	if err != nil {
		t.Fatalf("saveSound: %v", err)
	}
	if snd.ID != "page-002/3" || snd.Name != "Мой звук" || snd.Built {
		t.Fatalf("некорректные метаданные загруженного звука: %+v", snd)
	}

	// переименование
	if err := st.renameSound(snd.ID, "Выстрел"); err != nil {
		t.Fatalf("renameSound: %v", err)
	}

	// персональная громкость
	if err := st.setGain(snd.ID, 2.5); err != nil {
		t.Fatalf("setGain: %v", err)
	}
	if g := st.gainOf(snd.ID); g != 2.5 {
		t.Errorf("gainOf = %v, ожидалось 2.5", g)
	}

	// перемещение на занятый пэд — обмен местами
	other, err := st.saveSound("page-002", 5, "Второй.wav", renderDemo(demoTracks()[1]), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.moveSound(other.ID, "page-002", 3); err != nil {
		t.Fatalf("moveSound: %v", err)
	}
	pages, _ := st.snapshot()
	sounds := pages[1].Sounds
	if sounds[2].Name != "Второй" || sounds[4].Name != "Выстрел" {
		t.Fatalf("обмен не сработал: %q и %q", sounds[2].Name, sounds[4].Name)
	}
	if sounds[2].ID != "page-002/3" || sounds[4].ID != "page-002/5" {
		t.Fatalf("идентификаторы после переноса: %q, %q", sounds[2].ID, sounds[4].ID)
	}

	// удаление
	if err := st.deleteSound("page-002/3"); err != nil {
		t.Fatalf("deleteSound: %v", err)
	}
	pages, _ = st.snapshot()
	if pages[1].Sounds[2].ID != "" {
		t.Errorf("пэд не очистился: %+v", pages[1].Sounds[2])
	}
	if _, err := st.soundPath("page-002/3"); err == nil {
		t.Errorf("удалённый звук всё ещё доступен")
	}
}

func TestSettingsAreClamped(t *testing.T) {
	st, _ := testStore(t)
	if err := st.setVolume(1000); err != nil {
		t.Fatal(err)
	}
	if err := st.setPadVol(-4); err != nil {
		t.Fatal(err)
	}
	_, state := st.snapshot()
	if state.Volume != 300 {
		t.Errorf("громкость не ограничена сверху: %v", state.Volume)
	}
	if state.PadVol != 0 {
		t.Errorf("длительность не ограничена снизу: %v", state.PadVol)
	}
}

func TestPagesLifecycle(t *testing.T) {
	st, _ := testStore(t)
	name, err := st.addPage()
	if err != nil {
		t.Fatalf("addPage: %v", err)
	}
	pages, _ := st.snapshot()
	if len(pages) != 4 || name != "page-004" {
		t.Fatalf("страница не добавилась: %d, %q", len(pages), name)
	}
	if err := st.deletePage(name); err != nil {
		t.Fatalf("deletePage: %v", err)
	}
	pages, _ = st.snapshot()
	if len(pages) != 3 {
		t.Fatalf("страница не удалилась: %d", len(pages))
	}
	for len(pages) > 1 {
		if err := st.deletePage(pages[len(pages)-1].Name); err != nil {
			t.Fatalf("deletePage: %v", err)
		}
		pages, _ = st.snapshot()
	}
	if err := st.deletePage(pages[0].Name); err == nil {
		t.Errorf("последнюю страницу удалять нельзя")
	}
}

func TestHTTPAPI(t *testing.T) {
	st, p := testStore(t)
	srv := httptest.NewServer(newServer(st, p, log.New(io.Discard, "", 0)).routes())
	defer srv.Close()

	get := func(path string) map[string]any {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer res.Body.Close()
		var data map[string]any
		if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
			t.Fatalf("GET %s: не JSON: %v", path, err)
		}
		return data
	}
	post := func(path string, body any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		res, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return res
	}

	state := get("/api/state")
	pages, ok := state["pages"].([]any)
	if !ok || len(pages) != 3 {
		t.Fatalf("неожиданный /api/state: %v", state["pages"])
	}
	first := pages[0].(map[string]any)
	sounds := first["sounds"].([]any)
	if sounds[0] == nil {
		t.Fatalf("первый пэд пуст")
	}
	sound := sounds[0].(map[string]any)
	if sound["id"] != "page-001/1" {
		t.Fatalf("у звука нет id: %v", sound)
	}

	res := post("/api/sound", map[string]any{"action": "rename", "id": "page-001/1", "name": "Бочка"})
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("rename: %d %s", res.StatusCode, body)
	}
	res.Body.Close()

	state = get("/api/state")
	first = state["pages"].([]any)[0].(map[string]any)
	sound = first["sounds"].([]any)[0].(map[string]any)
	if sound["name"] != "Бочка" {
		t.Fatalf("имя не сохранилось: %v", sound["name"])
	}

	res = post("/api/volume", map[string]any{"value": 150})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("volume: %d", res.StatusCode)
	}
	res.Body.Close()
	if v := get("/api/state")["volume"].(float64); v != 150 {
		t.Fatalf("громкость не применилась: %v", v)
	}

	// загрузка файла через multipart
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("page", "page-002")
	mw.WriteField("slot", "1")
	part, _ := mw.CreateFormFile("file", "клик.wav")
	part.Write(renderDemo(demoTracks()[9]))
	mw.Close()
	res, err := http.Post(srv.URL+"/api/upload", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: %d %s", res.StatusCode, body)
	}
	res.Body.Close()

	state = get("/api/state")
	second := state["pages"].([]any)[1].(map[string]any)
	uploaded := second["sounds"].([]any)[0].(map[string]any)
	if uploaded["name"] != "клик" || uploaded["built"] != false {
		t.Fatalf("загруженный звук описан неверно: %v", uploaded)
	}

	// воспроизведение: без ffmpeg сервер честно сообщает об этом
	res = post("/api/play", map[string]any{"id": "page-001/1"})
	switch p.mode {
	case modeNone:
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("без ffmpeg ожидали 503, получили %d", res.StatusCode)
		}
	case modeFFmpeg:
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ffmpeg: ожидали 200, получили %d", res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "audio/wav" {
			t.Fatalf("ffmpeg: тип содержимого %q", ct)
		}
		data, _ := io.ReadAll(res.Body)
		if len(data) < 1000 {
			t.Fatalf("ffmpeg: подозрительно мало данных: %d байт", len(data))
		}
	}
	res.Body.Close()

	// несуществующий звук
	if res := post("/api/play", map[string]any{"id": "page-009/9"}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("несуществующий звук: ожидали 404, получили %d", res.StatusCode)
	} else {
		res.Body.Close()
	}
}

func TestAssetsAreEmbedded(t *testing.T) {
	for name, body := range map[string]string{"index.html": indexHTML, "app.js": appJS, "app.css": appCSS} {
		if len(body) < 200 {
			t.Errorf("встроенный ресурс %s пуст", name)
		}
	}
}
