package main

// store.go — хранилище библиотеки звуков.
//
// Каждая папка внутри каталога звуков считается «страницей» (как в Soundpad).
// Внутри папки лежат файлы .wav с числовым именем: 1.wav … 16.wav.
// Метаданные (названия звуков, заголовки страниц, цвета, громкость пэдов)
// лежат рядом в soundpad.json.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

const (
	maxPages      = 16
	maxSoundsPage = 16
	maxUploadSize = 64 << 20 // 64 МБ на звук
)

var (
	rePageName = regexp.MustCompile(`^page-\d{3}$`)
	reNumbered = regexp.MustCompile(`^(\d{1,3})\.wav$`)
)

// Sound — один звук на пэде.
type Sound struct {
	ID    string  `json:"id"`             // например "page-001/1"
	Name  string  `json:"name"`           // подпись на пэде
	Hue   int     `json:"hue"`            // цвет пэда
	Dur   float64 `json:"dur"`            // длительность, сек
	Built bool    `json:"built"`          // встроенный (демо) или загруженный
	Gain  float64 `json:"gain,omitempty"` // персональная громкость 0.1..3
}

// Page — одна страница (папка) с пэдами.
type Page struct {
	Name   string  `json:"name"`
	Title  string  `json:"title,omitempty"`
	Sounds []Sound `json:"sounds"` // индекс = номер пэда; пустой ID — пэд свободен
}

type soundMeta struct {
	Name  string  `json:"name"`
	Hue   int     `json:"hue"`
	Gain  float64 `json:"gain,omitempty"`
	Built bool    `json:"built,omitempty"`
}

type pageMeta struct {
	Name   string      `json:"name"`
	Title  string      `json:"title,omitempty"`
	Sounds []soundMeta `json:"sounds,omitempty"`
}

// State — то, что сохраняется в soundpad.json.
type State struct {
	Volume float64    `json:"volume"` // общая громкость, % (0..300)
	PadVol float64    `json:"padVol"` // длительность пэда, сек (0.2..8; 0 = до конца)
	Pages  []pageMeta `json:"pages,omitempty"`
}

type store struct {
	dir    string
	player *player

	mu    sync.Mutex
	pages []*Page
	state State
}

func newStore(dir string, p *player) (*store, error) {
	s := &store{
		dir:    dir,
		player: p,
		state:  State{Volume: 100, PadVol: 3},
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("не удалось создать каталог звуков: %w", err)
	}
	if err := s.loadState(); err != nil {
		return nil, err
	}
	if err := s.seedDemo(); err != nil {
		return nil, err
	}
	if err := s.scan(); err != nil {
		return nil, err
	}
	p.setGlobals(s.state.Volume, s.state.PadVol)
	return s, nil
}

// ── состояние ──────────────────────────────────────────────────────────────

func (s *store) statePath() string { return filepath.Join(s.dir, "soundpad.json") }

func (s *store) loadState() error {
	raw, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("не удалось прочитать состояние: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil // битый файл не повод не запускаться
	}
	if st.Volume < 0 || st.Volume > 300 {
		st.Volume = 100
	}
	if st.PadVol < 0 || st.PadVol > 8 {
		st.PadVol = 3
	}
	s.state = st
	return nil
}

// saveStateLocked сохраняет состояние. Вызывается с захваченным мьютексом.
func (s *store) saveStateLocked() error {
	metas := make([]pageMeta, 0, len(s.pages))
	for _, p := range s.pages {
		pm := pageMeta{Name: p.Name, Title: p.Title}
		for _, snd := range p.Sounds {
			if snd.ID == "" {
				pm.Sounds = append(pm.Sounds, soundMeta{})
				continue
			}
			pm.Sounds = append(pm.Sounds, soundMeta{Name: snd.Name, Hue: snd.Hue, Gain: snd.Gain, Built: snd.Built})
		}
		metas = append(metas, pm)
	}
	s.state.Pages = metas

	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// seedDemo один раз синтезирует встроенные звуки в папку page-001.
func (s *store) seedDemo() error {
	dir := filepath.Join(s.dir, "page-001")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i, t := range demoTracks() {
		path := filepath.Join(dir, strconv.Itoa(i+1)+".wav")
		if _, err := os.Stat(path); err == nil {
			continue // уже есть — не перезаписываем
		}
		if err := os.WriteFile(path, renderDemo(t), 0o644); err != nil {
			return fmt.Errorf("не удалось записать демо-звук %q: %w", t.Name, err)
		}
	}
	// страницы 2 и 3 — пустые, чтобы было куда складывать своё
	for _, n := range []int{2, 3} {
		if err := os.MkdirAll(filepath.Join(s.dir, fmt.Sprintf("page-%03d", n)), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// ── сканирование ───────────────────────────────────────────────────────────

// scan пересобирает список страниц по содержимому каталога, сохраняя
// метаданные из soundpad.json.
func (s *store) scan() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && rePageName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	old := map[string]*Page{}
	for _, p := range s.pages {
		old[p.Name] = p
	}
	meta := map[string]pageMeta{}
	for _, pm := range s.state.Pages {
		meta[pm.Name] = pm
	}

	demos := demoTracks()
	pages := make([]*Page, 0, len(names))
	for _, name := range names {
		page := &Page{Name: name, Sounds: make([]Sound, maxSoundsPage)}
		if prev, ok := old[name]; ok {
			page.Title = prev.Title
			for i := range page.Sounds {
				if i < len(prev.Sounds) {
					page.Sounds[i] = prev.Sounds[i]
				}
			}
		} else if pm, ok := meta[name]; ok {
			page.Title = pm.Title
			for i, sm := range pm.Sounds {
				if i >= maxSoundsPage || sm.Name == "" {
					continue
				}
				page.Sounds[i] = Sound{
					ID:    name + "/" + strconv.Itoa(i+1),
					Name:  sm.Name,
					Hue:   sm.Hue,
					Built: sm.Built,
					Gain:  sm.Gain,
				}
			}
		}

		files, err := os.ReadDir(filepath.Join(s.dir, name))
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			m := reNumbered.FindStringSubmatch(f.Name())
			if m == nil {
				continue
			}
			num, _ := strconv.Atoi(m[1])
			if num < 1 || num > maxSoundsPage {
				continue
			}
			// файла нет, а пэд в метаданных занят — считаем звук потерянным
			snd := &page.Sounds[num-1]
			if _, err := os.Stat(filepath.Join(s.dir, name, f.Name())); err != nil {
				*snd = Sound{}
				continue
			}
			if snd.Name == "" {
				label, hue := defaultMeta(demos, name, num)
				*snd = Sound{
					ID:    name + "/" + strconv.Itoa(num),
					Name:  label,
					Hue:   hue,
					Built: name == "page-001",
				}
			}
			if dur, err := wavDuration(filepath.Join(s.dir, name, f.Name())); err == nil {
				snd.Dur = dur
			}
		}
		pages = append(pages, page)
	}
	s.pages = pages
	return nil
}

// defaultMeta подбирает подпись и цвет для пэда, потерявшего метаданные:
// для первой страницы это названия встроенных демо-звуков.
func defaultMeta(demos []demoTrack, pageName string, num int) (string, int) {
	if pageName == "page-001" && num <= len(demos) {
		return demos[num-1].Name, demos[num-1].Hue
	}
	return fmt.Sprintf("Sound %d", num), (num*97 + 25) % 360
}

// ── операции над страницами и пэдами ───────────────────────────────────────

func (s *store) snapshot() ([]*Page, State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Page, len(s.pages))
	for i, p := range s.pages {
		cp := *p
		cp.Sounds = append([]Sound(nil), p.Sounds...)
		out[i] = &cp
	}
	st := s.state
	st.Pages = nil
	return out, st
}

func (s *store) setVolume(v float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 0 {
		v = 0
	}
	if v > 300 {
		v = 300
	}
	s.state.Volume = v
	s.player.setGlobals(s.state.Volume, s.state.PadVol)
	return s.saveStateLocked()
}

func (s *store) setPadVol(v float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v < 0 {
		v = 0
	}
	if v > 8 {
		v = 8
	}
	s.state.PadVol = v
	s.player.setGlobals(s.state.Volume, s.state.PadVol)
	return s.saveStateLocked()
}

func (s *store) setPageTitle(name, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pageByNameLocked(name)
	if p == nil {
		return errors.New("страница не найдена")
	}
	if len(title) > 32 {
		title = title[:32]
	}
	p.Title = title
	return s.saveStateLocked()
}

// pageByNameLocked ищет страницу. Вызывается с захваченным мьютексом.
func (s *store) pageByNameLocked(name string) *Page {
	for _, p := range s.pages {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func (s *store) addPage() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pages) >= maxPages {
		return "", errors.New("достигнут предел в 16 страниц")
	}
	for n := 1; n <= maxPages; n++ {
		cand := fmt.Sprintf("page-%03d", n)
		if s.pageByNameLocked(cand) == nil {
			if err := os.MkdirAll(filepath.Join(s.dir, cand), 0o755); err != nil {
				return "", err
			}
			if err := s.scan(); err != nil {
				return "", err
			}
			_ = s.saveStateLocked()
			return cand, nil
		}
	}
	return "", errors.New("нет свободных номеров страниц")
}

// deletePage удаляет папку страницы вместе со звуками.
func (s *store) deletePage(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pageByNameLocked(name) == nil {
		return errors.New("страница не найдена")
	}
	if len(s.pages) <= 1 {
		return errors.New("нельзя удалить последнюю страницу")
	}
	if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
		return err
	}
	if err := s.scan(); err != nil {
		return err
	}
	return s.saveStateLocked()
}

func (s *store) soundLocked(id string) (*Page, int, *Sound) {
	for _, p := range s.pages {
		for i := range p.Sounds {
			if p.Sounds[i].ID == id {
				return p, i, &p.Sounds[i]
			}
		}
	}
	return nil, -1, nil
}

// saveSound сохраняет загруженный аудиофайл и привязывает его к пэду num.
// dur — длительность, уже определённая декодером (0 = неизвестна).
func (s *store) saveSound(pageName string, num int, filename string, data []byte, dur float64) (*Sound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pageByNameLocked(pageName)
	if p == nil {
		return nil, errors.New("страница не найдена")
	}
	if num < 1 || num > maxSoundsPage {
		return nil, errors.New("номер пэда вне диапазона 1..16")
	}
	path := filepath.Join(s.dir, pageName, strconv.Itoa(num)+".wav")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	idx := num - 1
	snd := &p.Sounds[idx]
	base := filename
	if ext := filepath.Ext(base); ext != "" {
		base = base[:len(base)-len(ext)]
	}
	if len(base) > 48 {
		base = base[:48]
	}
	*snd = Sound{
		ID:    pageName + "/" + strconv.Itoa(num),
		Name:  base,
		Hue:   (num*137 + 40) % 360,
		Built: false,
	}
	if dur <= 0 {
		if d, err := wavDuration(path); err == nil {
			dur = d
		}
	}
	snd.Dur = dur
	if err := s.saveStateLocked(); err != nil {
		return nil, err
	}
	return snd, nil
}

func (s *store) renameSound(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, snd := s.soundLocked(id)
	if snd == nil {
		return errors.New("звук не найден")
	}
	if name == "" {
		return errors.New("пустое имя")
	}
	if len(name) > 48 {
		name = name[:48]
	}
	snd.Name = name
	return s.saveStateLocked()
}

func (s *store) setGain(id string, gain float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, snd := s.soundLocked(id)
	if snd == nil {
		return errors.New("звук не найден")
	}
	if gain < 0.1 {
		gain = 0.1
	}
	if gain > 3 {
		gain = 3
	}
	snd.Gain = gain
	return s.saveStateLocked()
}

func (s *store) deleteSound(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, idx, snd := s.soundLocked(id)
	if snd == nil {
		return errors.New("звук не найден")
	}
	if err := os.RemoveAll(filepath.Join(s.dir, p.Name, strconv.Itoa(idx+1)+".wav")); err != nil {
		return err
	}
	p.Sounds[idx] = Sound{}
	return s.saveStateLocked()
}

// moveSound переносит звук на другой пэд/страницу, меняя файлы местами.
func (s *store) moveSound(fromID string, toPage string, toNum int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fp, fi, fsnd := s.soundLocked(fromID)
	if fsnd == nil {
		return errors.New("звук не найден")
	}
	tp := s.pageByNameLocked(toPage)
	if tp == nil {
		return errors.New("страница не найдена")
	}
	if toNum < 1 || toNum > maxSoundsPage {
		return errors.New("номер пэда вне диапазона")
	}
	ti := toNum - 1
	if fp.Name == tp.Name && fi == ti {
		return nil
	}

	fromPath := filepath.Join(s.dir, fp.Name, strconv.Itoa(fi+1)+".wav")
	toPath := filepath.Join(s.dir, tp.Name, strconv.Itoa(ti+1)+".wav")

	// Свободен ли целевой пэд? Если нет — меняемся местами.
	if tp.Sounds[ti].ID == "" {
		if err := os.Rename(fromPath, toPath); err != nil {
			return err
		}
		tp.Sounds[ti], fp.Sounds[fi] = *fsnd, Sound{}
	} else {
		tmp := fromPath + ".swap"
		if err := os.Rename(toPath, tmp); err != nil {
			return err
		}
		if err := os.Rename(fromPath, toPath); err != nil {
			return err
		}
		if err := os.Rename(tmp, fromPath); err != nil {
			return err
		}
		target := tp.Sounds[ti]
		tp.Sounds[ti], fp.Sounds[fi] = *fsnd, target
	}
	// ID привязан к месту, поэтому обновляем его.
	tp.Sounds[ti].ID = tp.Name + "/" + strconv.Itoa(ti+1)
	fp.Sounds[fi].ID = fp.Name + "/" + strconv.Itoa(fi+1)
	return s.saveStateLocked()
}

// ── путь к файлу звука ─────────────────────────────────────────────────────

// soundPath проверяет, что ID существует в библиотеке, и возвращает путь к файлу.
func (s *store) soundPath(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, idx, snd := s.soundLocked(id)
	if snd == nil || snd.ID == "" {
		return "", errors.New("звук не найден")
	}
	return filepath.Join(s.dir, p.Name, strconv.Itoa(idx+1)+".wav"), nil
}

// gainOf возвращает персональное усиление пэда (по умолчанию 1).
func (s *store) gainOf(id string) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, snd := s.soundLocked(id)
	if snd == nil || snd.Gain == 0 {
		return 1
	}
	return snd.Gain
}
