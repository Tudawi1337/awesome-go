package main

// wav.go — минимальный разбор WAV-заголовка: нужен, чтобы показать
// длительность звука на пэде.

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const wavHeaderSize = 12

var errNotWAV = errors.New("не WAV-файл")

// wavDuration возвращает длительность WAV-файла в секундах.
// Поддерживаются контейнеры RIFF/RF64 с несжатым PCM или IEEE-float.
func wavDuration(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	head := make([]byte, wavHeaderSize)
	if _, err := io.ReadFull(f, head); err != nil {
		return 0, err
	}
	riff := string(head[0:4])
	if (riff != "RIFF" && riff != "RF64") || string(head[8:12]) != "WAVE" {
		return 0, errNotWAV
	}

	var (
		haveFmt      bool
		blockAlign   uint16
		byteRate     uint32
		dataSize     uint32
		pcmFormat    uint16
		haveDataSize bool
	)
	buf := make([]byte, 8)
	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			break
		}
		id := string(buf[0:4])
		size := binary.LittleEndian.Uint32(buf[4:8])
		switch id {
		case "fmt ":
			chunk := make([]byte, size)
			if _, err := io.ReadFull(f, chunk); err != nil {
				return 0, err
			}
			if len(chunk) < 16 {
				return 0, errNotWAV
			}
			pcmFormat = binary.LittleEndian.Uint16(chunk[0:2])
			byteRate = binary.LittleEndian.Uint32(chunk[8:12])
			blockAlign = binary.LittleEndian.Uint16(chunk[12:14])
			haveFmt = true
			if size%2 == 1 {
				f.Seek(1, io.SeekCurrent)
			}
			continue
		case "data":
			dataSize, haveDataSize = size, true
			// к данным не переходим — длительность уже известна из размера
			if haveFmt && byteRate > 0 {
				return float64(dataSize) / float64(byteRate), nil
			}
			if _, err := f.Seek(int64(size), io.SeekCurrent); err != nil {
				return 0, err
			}
			if size%2 == 1 {
				f.Seek(1, io.SeekCurrent)
			}
			continue
		}
		if _, err := f.Seek(int64(size), io.SeekCurrent); err != nil {
			return 0, err
		}
		if size%2 == 1 { // чанки выравниваются по чётной границе
			f.Seek(1, io.SeekCurrent)
		}
	}
	if !haveFmt || !haveDataSize {
		return 0, errNotWAV
	}
	if pcmFormat != 1 && pcmFormat != 3 || byteRate == 0 {
		// не PCM/float или неизвестный байтрейт — оцениваем по blockAlign
		if blockAlign == 0 || dataSize == 0 {
			return 0, errNotWAV
		}
	}
	if byteRate == 0 {
		return 0, errNotWAV
	}
	return float64(dataSize) / float64(byteRate), nil
}
