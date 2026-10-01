package main

// synth.go — генератор демо-звуков. При первом запуске Soundpad сам
// синтезирует набор «пэдовых» сэмплов (барабаны, 8-битные эффекты и т.п.),
// чтобы программой можно было пользоваться сразу, без готовых файлов.
// Всё считается на месте и записывается в WAV (PCM 16 бит, моно, 44.1 кГц).

import (
	"encoding/binary"
	"math"
	"math/rand"
)

const sampleRate = 44100

// demoTrack описывает один встроенный сэмпл.
type demoTrack struct {
	Slug string           // часть идентификатора: demo-<slug>
	Name string           // подпись на пэде
	Hue  int              // цвет пэда (HSV-оттенок 0..360)
	Gen  func() []float64 // генератор сэмплов в диапазоне [-1, 1]
}

// demoTracks возвращает 16 встроенных звуков — ровно на один экран пэдов 4×4.
func demoTracks() []demoTrack {
	s := newSynth()
	return []demoTrack{
		{"kick", "Kick", 15, s.kick},
		{"snare", "Snare", 0, s.snare},
		{"hihat", "Hi-Hat", 45, s.hat},
		{"openhat", "Open Hat", 45, s.openHat},
		{"clap", "Clap", 330, s.clap},
		{"tom", "Tom", 25, s.tom},
		{"rim", "Rim", 190, s.rim},
		{"cowbell", "Cowbell", 60, s.cowbell},
		{"laser", "Laser", 285, s.laser},
		{"coin", "Coin", 50, s.coin},
		{"bell", "Bell", 205, s.bell},
		{"airhorn", "Airhorn", 350, s.airhorn},
		{"powerup", "Power Up", 120, s.powerUp},
		{"boing", "Boing", 165, s.boing},
		{"drumloop", "Drum Loop", 220, s.drumLoop},
		{"applause", "Applause", 240, s.applause},
	}
}

type synth struct {
	rate int
	rnd  *rand.Rand
}

func newSynth() *synth {
	return &synth{rate: sampleRate, rnd: rand.New(rand.NewSource(20240501))}
}

func (s *synth) buf(seconds float64) []float64 {
	return make([]float64, int(seconds*float64(s.rate)))
}

// noise — белый шум в диапазоне [-1, 1].
func (s *synth) noise() float64 { return s.rnd.Float64()*2 - 1 }

// env — простая AD-огибающая: быстрая атака, экспоненциальный спад.
func env(t, decay float64) float64 { return math.Exp(-t * decay) }

func square(phase float64) float64 {
	if math.Mod(phase, 2*math.Pi) < math.Pi {
		return 1
	}
	return -1
}

func saw(phase float64) float64 {
	p := math.Mod(phase, 2*math.Pi) / (2 * math.Pi)
	return 2*p - 1
}

// kick — бочка: синус со спадом частоты 150 → 45 Гц плюс щелчок.
func (s *synth) kick() []float64 {
	out := s.buf(0.45)
	phase := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		f := 45 + 105*math.Exp(-t*30)
		phase += 2 * math.Pi * f / float64(s.rate)
		out[i] = math.Sin(phase)*env(t, 6.5) + 0.35*math.Exp(-t*140)*s.noise()
	}
	return out
}

// snare — малый барабан: шум + тон 190 Гц.
func (s *synth) snare() []float64 {
	out := s.buf(0.3)
	prev := 0.0
	phase := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		n := s.noise()
		hp := n - prev // грубый фильтр высоких частот
		prev = n
		phase += 2 * math.Pi * 190 / float64(s.rate)
		out[i] = 0.8*hp*env(t, 20) + 0.35*math.Sin(phase)*env(t, 32)
	}
	return out
}

func (s *synth) hat() []float64 {
	out := s.buf(0.09)
	prev := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		n := s.noise()
		hp := n - prev
		prev = n
		out[i] = hp * env(t, 85)
	}
	return out
}

func (s *synth) openHat() []float64 {
	out := s.buf(0.45)
	prev := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		n := s.noise()
		hp := n - prev
		prev = n
		out[i] = hp * env(t, 9)
	}
	return out
}

// clap — хлопок: несколько коротких всплесков шума и «хвост».
func (s *synth) clap() []float64 {
	out := s.buf(0.35)
	prev := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		burst := 0.0
		for k := 0; k < 4; k++ {
			d := t - float64(k)*0.011
			if d >= 0 {
				burst += math.Exp(-d * d / (2 * 0.0004 * 0.0004))
			}
		}
		tail := 0.0
		if t > 0.033 {
			tail = math.Exp(-(t - 0.033) * 18)
		}
		n := s.noise()
		hp := n - 0.6*prev
		prev = n
		out[i] = hp * (0.9*burst + 0.7*tail)
	}
	return out
}

// tom — том: спуск 200 → 110 Гц.
func (s *synth) tom() []float64 {
	out := s.buf(0.45)
	phase := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		f := 110 + 90*math.Exp(-t*9)
		phase += 2 * math.Pi * f / float64(s.rate)
		out[i] = math.Sin(phase)*env(t, 7) + 0.1*s.noise()*env(t, 40)
	}
	return out
}

func (s *synth) rim() []float64 {
	out := s.buf(0.08)
	for i := range out {
		t := float64(i) / float64(s.rate)
		out[i] = 0.8*math.Sin(2*math.Pi*1700*t)*env(t, 90) + 0.5*s.noise()*env(t, 160)
	}
	return out
}

// cowbell — коробель: две прямоугольные волны 587 и 845 Гц.
func (s *synth) cowbell() []float64 {
	out := s.buf(0.5)
	for i := range out {
		t := float64(i) / float64(s.rate)
		a := square(2*math.Pi*587*t) + square(2*math.Pi*845*t)
		out[i] = 0.4 * a * (env(t, 9) - 0.3*env(t, 40))
	}
	return out
}

// laser — нисходящий «пиу» на прямоугольной волне.
func (s *synth) laser() []float64 {
	out := s.buf(0.35)
	phase := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		f := 180 + 1600*math.Exp(-t*7)
		phase += 2 * math.Pi * f / float64(s.rate)
		out[i] = 0.7 * square(phase) * env(t, 9)
	}
	return out
}

// coin — «монетка»: две ноты B5 → E6.
func (s *synth) coin() []float64 {
	out := s.buf(0.45)
	phase := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		f, dec := 987.77, 9.0
		if t > 0.07 {
			f, dec = 1318.51, 5.5
		}
		phase += 2 * math.Pi * f / float64(s.rate)
		out[i] = 0.55 * square(phase) * env(t, dec)
	}
	return out
}

// bell — колокол: набор обертонов с разными временами затухания.
func (s *synth) bell() []float64 {
	out := s.buf(1.6)
	ratios := []float64{1, 2.0, 2.76, 5.4, 8.93}
	amps := []float64{1, 0.55, 0.35, 0.2, 0.12}
	decays := []float64{2.2, 2.8, 3.4, 5, 6.5}
	for i := range out {
		t := float64(i) / float64(s.rate)
		v := 0.0
		for k, r := range ratios {
			v += amps[k] * math.Sin(2*math.Pi*523.25*r*t) * env(t, decays[k])
		}
		out[i] = v
	}
	return out
}

// airhorn — пила с вибрато и лёгким перегрузом.
func (s *synth) airhorn() []float64 {
	out := s.buf(0.9)
	for i := range out {
		t := float64(i) / float64(s.rate)
		vib := 1 + 0.015*math.Sin(2*math.Pi*6.2*t)
		v := saw(2*math.Pi*415*t*vib) + saw(2*math.Pi*622*t*vib)
		a := math.Min(1, t/0.02)
		if d := 0.9 - t; d < 0.12 {
			a *= math.Max(0, d/0.12)
		}
		out[i] = math.Tanh(v*1.4) * a
	}
	return out
}

// powerUp — быстрое восходящее арпеджио.
func (s *synth) powerUp() []float64 {
	notes := []float64{523.25, 659.25, 783.99, 1046.5, 1318.5, 1568, 2093}
	out := s.buf(0.6)
	step := 0.06
	for i := range out {
		t := float64(i) / float64(s.rate)
		n := int(t / step)
		if n >= len(notes) {
			n = len(notes) - 1
		}
		local := t - float64(n)*step
		phase := 2 * math.Pi * notes[n] * local
		out[i] = 0.6 * saw(phase) * env(local, 22)
	}
	return out
}

// boing — «пружина»: затухающая вибрация частоты.
func (s *synth) boing() []float64 {
	out := s.buf(0.5)
	phase := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		f := 300 + 220*math.Exp(-4*t)*math.Cos(2*math.Pi*9*t)
		phase += 2 * math.Pi * f / float64(s.rate)
		out[i] = math.Sin(phase) * env(t, 4.5)
	}
	return out
}

// mix подмешивает src в dst со сдвигом (в секундах) и усилением.
func mix(dst, src []float64, offsetSec, gain float64) {
	start := int(offsetSec * float64(sampleRate))
	for i, v := range src {
		j := start + i
		if j < 0 || j >= len(dst) {
			continue
		}
		dst[j] += v * gain
	}
}

// drumLoop — один такт 120 BPM: бочка, снейр, хэты.
func (s *synth) drumLoop() []float64 {
	out := s.buf(2.0)
	kick, snare, hat, ohat := s.kick(), s.snare(), s.hat(), s.openHat()
	mix(out, kick, 0.0, 1.0)
	mix(out, kick, 1.0, 1.0)
	mix(out, snare, 0.5, 0.8)
	mix(out, snare, 1.5, 0.8)
	for b := 0.0; b < 2.0; b += 0.25 {
		mix(out, hat, b, 0.5)
	}
	mix(out, ohat, 1.75, 0.6)
	return out
}

// applause — аплодисменты: шумовой «гул толпы» плюс случайные хлопки.
func (s *synth) applause() []float64 {
	out := s.buf(1.8)
	lp := 0.0
	pop := 0.0
	for i := range out {
		t := float64(i) / float64(s.rate)
		n := s.noise()
		lp += 0.12 * (n - lp) // сглаженный шум — «толпа»
		if s.rnd.Float64() < 0.02 {
			pop += 0.7 * s.rnd.Float64()
		}
		pop *= 0.992
		fade := math.Min(1, t/0.15)
		if d := 1.8 - t; d < 0.3 {
			fade *= math.Max(0, d/0.3)
		}
		out[i] = (lp*2.2 + pop*n) * fade
	}
	return out
}

// normalize масштабирует буфер так, чтобы пик был равен peak, и мягко
// ограничивает выбросы, чтобы не было цифрового перегруза.
func normalize(in []float64, peak float64) []float64 {
	max := 0.0
	for _, v := range in {
		if a := math.Abs(v); a > max {
			max = a
		}
	}
	if max == 0 {
		return in
	}
	g := peak / max
	for i, v := range in {
		in[i] = math.Tanh(v * g * 1.15)
	}
	return in
}

// encodeWAV упаковывает сэмплы [-1, 1] в WAV-файл: PCM 16 бит, моно.
func encodeWAV(samples []float64) []byte {
	data := make([]byte, len(samples)*2)
	for i, v := range samples {
		if v > 1 {
			v = 1
		} else if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(data[i*2:], uint16(int16(math.Round(v*32767))))
	}
	out := make([]byte, 0, 44+len(data))
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(data)))
	out = append(out, "WAVEfmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)           // размер fmt-блока
	out = binary.LittleEndian.AppendUint16(out, 1)            // PCM
	out = binary.LittleEndian.AppendUint16(out, 1)            // каналов
	out = binary.LittleEndian.AppendUint32(out, sampleRate)   // частота
	out = binary.LittleEndian.AppendUint32(out, sampleRate*2) // байт в секунду
	out = binary.LittleEndian.AppendUint16(out, 2)            // выравнивание
	out = binary.LittleEndian.AppendUint16(out, 16)           // бит на сэмпл
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

// renderDemo синтезирует и кодирует один встроенный звук.
func renderDemo(t demoTrack) []byte { return encodeWAV(normalize(t.Gen(), 0.95)) }
