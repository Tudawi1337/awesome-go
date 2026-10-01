package main

// player.go — воспроизведение звуков.
//
// Приоритетный вариант — ffmpeg: он декодирует что угодно (mp3, ogg, m4a,
// flac, opus…) и умеет менять громкость и обрезать звук по длительности
// пэда. Результат декодируется в WAV и отдаётся браузеру, который и
// воспроизводит звук (в том числе в микрофон/стереомикшер через настройки ОС).
// Если ffmpeg нет, пробуем ffplay (он играет с сервера напрямую).
// Если нет и его — интерфейс проигрывает WAV-файлы напрямую через <audio>.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// reFFmpegDuration вытаскивает «Duration: 00:00:01.50» из лога ffmpeg.
var reFFmpegDuration = regexp.MustCompile(`Duration:\s*(\d+):(\d\d):(\d+(?:\.\d+)?)`)

type playMode int

const (
	modeNone playMode = iota
	modeFFmpeg
	modeFFplay
)

func (m playMode) available() bool { return m != modeNone }

const (
	maxConcurrent  = 32
	maxStreamBytes = 32 << 20 // 32 МБ декодированного звука на один запуск
)

type running struct {
	seq     int
	soundID string
	cancel  context.CancelFunc
}

type player struct {
	mode       playMode
	ffmpegPath string
	ffplayPath string
	log        *log.Logger

	mu      sync.Mutex
	volume  float64 // общая громкость, %
	padVol  float64 // длительность пэда, сек (0 = до конца)
	running map[int]*running
	nextSeq int
}

func newPlayer(l *log.Logger) *player {
	p := &player{
		log:     l,
		volume:  100,
		padVol:  3,
		running: map[int]*running{},
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		p.mode, p.ffmpegPath = modeFFmpeg, path
		if fp, err := exec.LookPath("ffplay"); err == nil {
			p.ffplayPath = fp
		}
	} else if fp, err := exec.LookPath("ffplay"); err == nil {
		p.mode, p.ffplayPath = modeFFplay, fp
	}
	return p
}

// setServerOutput переключает воспроизведение на сервер (ffplay): звук идёт
// в звуковое устройство машины, где запущен Soundpad, — это удобно, если
// браузер открыт на другом компьютере или звук нужно завернуть в микрофон.
func (p *player) setServerOutput(on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !on {
		if p.ffmpegPath != "" {
			p.mode = modeFFmpeg
		} else {
			p.mode = modeNone
		}
		return nil
	}
	if p.ffplayPath == "" {
		return errors.New("ffplay не найден: серверное воспроизведение недоступно")
	}
	p.mode = modeFFplay
	return nil
}

func (p *player) setGlobals(volume, padVol float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.volume, p.padVol = volume, padVol
}

// clampVolume приводит итоговую громкость к разумным границам.
func clampVolume(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 10 {
		return 10
	}
	return v
}

// effectiveVolume считает итоговое усиление: общая громкость × усиление пэда.
func (p *player) currentMode() playMode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mode
}

func (p *player) effectiveVolume(gain float64) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gain <= 0 {
		gain = 1
	}
	return clampVolume(p.volume / 100.0 * gain)
}

// play запускает звук средствами сервера (режим ffplay).
// В режиме ffmpeg звук отдаётся браузеру, поэтому здесь не используется.
// mode: "toggle" (повторное нажатие останавливает), "overlap" (каждый запуск —
// новая копия) или "stop". Возвращает признак «играет сейчас» и число
// остановленных ранее копий.
func (p *player) play(path, soundID, mode string, gain float64) (bool, int, error) {
	if p.mode != modeFFplay {
		return false, 0, errors.New("серверное воспроизведение доступно только в режиме ffplay")
	}
	volume := p.effectiveVolume(gain)

	p.mu.Lock()
	stopped := p.stopBySoundLocked(soundID)
	if mode == "stop" {
		p.mu.Unlock()
		return false, stopped, nil
	}
	if mode == "toggle" && stopped > 0 {
		p.mu.Unlock()
		return false, stopped, nil
	}
	p.mu.Unlock()

	// Ограничиваем число одновременных звуков: самый старый глушим.
	if p.activeCount() >= maxConcurrent {
		p.killOldest()
	}

	p.mu.Lock()
	p.nextSeq++
	seq := p.nextSeq
	p.running[seq] = &running{seq: seq, soundID: soundID}
	p.mu.Unlock()

	err := p.spawnFFplay(path, volume)

	p.mu.Lock()
	delete(p.running, seq)
	p.mu.Unlock()

	if err != nil {
		return false, stopped, err
	}
	return true, stopped, nil
}

func (p *player) killOldest() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.running) == 0 {
		return
	}
	oldestSeq := -1
	for seq := range p.running {
		if oldestSeq == -1 || seq < oldestSeq {
			oldestSeq = seq
		}
	}
	if r, ok := p.running[oldestSeq]; ok {
		if r.cancel != nil {
			r.cancel()
		}
		delete(p.running, oldestSeq)
	}
}

// decodeOnce декодирует файл в WAV с заданной громкостью, обрезая по
// длительности пэда. Возвращает готовые байты (≤ maxStreamBytes).
func (p *player) decodeOnce(path string, volume float64) ([]byte, error) {
	p.mu.Lock()
	padVol := p.padVol
	p.mu.Unlock()

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", path}
	args = append(args, "-af", fmt.Sprintf("volume=%.4f", volume))
	if padVol > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", padVol))
	}
	args = append(args, "-f", "wav", "pipe:1")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.ffmpegPath, args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxStreamBytes, 8<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("ffmpeg не успел обработать звук за 90 секунд")
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg: %s", msg)
	}
	if stdout.Len() == 0 {
		return nil, errors.New("ffmpeg вернул пустой звук")
	}
	return stdout.Bytes(), nil
}

// spawnFFplay играет файл напрямую через ffplay (резервный режим).
func (p *player) spawnFFplay(path string, volume float64) error {
	p.mu.Lock()
	padVol := p.padVol
	p.mu.Unlock()

	args := []string{"-hide_banner", "-nodisp", "-autoexit", "-loglevel", "error"}
	args = append(args, "-volume", fmt.Sprintf("%d", int(math.Round(math.Min(volume, 1)*100))))
	if padVol > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", padVol))
	}
	args = append(args, "-i", path)

	cmd := exec.Command(p.ffplayPath, args...)
	var stderr limitedBuffer
	stderr.limit = 8 << 10
	cmd.Stderr = &stderr
	cmd.Stdin = nil // /dev/null: ffplay не ждёт клавиатуру
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось запустить ffplay: %w", err)
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				p.log.Printf("ffplay: %s", msg)
			}
		}
	}()
	return nil
}

// clipForBrowser готовит звук к воспроизведению в браузере: при наличии
// ffmpeg декодирует в WAV с нужной громкостью, иначе отдаёт файл как есть.
func (p *player) clipForBrowser(path string, gain float64) ([]byte, string, error) {
	if p.currentMode() == modeFFmpeg {
		data, err := p.decodeOnce(path, p.effectiveVolume(gain))
		return data, "audio/wav", err
	}
	raw, err := os.ReadFile(path)
	return raw, "application/octet-stream", err
}

// stopBySoundLocked глушит все копии звука. Вызывается с захваченным мьютексом.
func (p *player) stopBySoundLocked(soundID string) int {
	n := 0
	for seq, r := range p.running {
		if r.soundID == soundID {
			if r.cancel != nil {
				r.cancel()
			}
			delete(p.running, seq)
			n++
		}
	}
	return n
}

func (p *player) stopAll() {
	p.mu.Lock()
	for seq, r := range p.running {
		if r.cancel != nil {
			r.cancel()
		}
		delete(p.running, seq)
	}
	p.mu.Unlock()
}

func (p *player) activeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.running)
}

// probeFile прогоняет файл через декодер: заодно ловит битые/незвуковые
// файлы и вытаскивает длительность (для форматов, которые не разбирает
// собственный парсер WAV).
func (p *player) probeFile(path string) (float64, error) {
	if p.mode != modeFFmpeg {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.ffmpegPath,
		"-hide_banner", "-nostdin", "-i", path, "-f", "null", "-")
	var stderr limitedBuffer
	stderr.limit = 16 << 10
	cmd.Stderr = &stderr
	err := cmd.Run()
	msg := stderr.String()
	if m := reFFmpegDuration.FindStringSubmatch(msg); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		sec, _ := strconv.ParseFloat(m[3], 64)
		return float64(h)*3600 + float64(min)*60 + sec, nil
	}
	if err != nil {
		text := strings.TrimSpace(msg)
		if text == "" {
			text = err.Error()
		}
		return 0, errors.New(text)
	}
	return 0, nil
}

// limitedBuffer — буфер, который молча отбрасывает данные сверх лимита.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit > 0 && b.Buffer.Len()+len(p) > b.limit {
		space := b.limit - b.Buffer.Len()
		if space > 0 {
			b.Buffer.Write(p[:space])
		}
		return len(p), nil // делаем вид, что всё приняли
	}
	return b.Buffer.Write(p)
}
