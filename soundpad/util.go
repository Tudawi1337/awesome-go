package main

// util.go — мелкие вспомогательные функции.

import (
	"os"
	"path/filepath"
)

// writeTemp сохраняет данные во временный файл и возвращает путь к нему.
func writeTemp(data []byte) (string, error) {
	f, err := os.CreateTemp("", "soundpad-*.audio")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func removeQuiet(path string) { _ = os.Remove(path) }

// humanSize форматирует размер для логов и интерфейса.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " Б"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	units := []string{"КБ", "МБ", "ГБ", "ТБ"}
	val := float64(n) / float64(div)
	return trimFloat(val) + " " + units[exp]
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func trimFloat(v float64) string {
	if v == float64(int64(v)) {
		return itoa(int64(v))
	}
	out := make([]byte, 0, 6)
	whole := int64(v)
	out = append(out, itoa(whole)...)
	frac := int64((v - float64(whole)) * 10)
	if frac == 0 {
		return string(out)
	}
	out = append(out, '.')
	out = append(out, byte('0'+frac))
	return string(out)
}

// ensureDir создаёт каталог, если его нет.
func ensureDir(path string) error { return os.MkdirAll(filepath.Clean(path), 0o755) }
