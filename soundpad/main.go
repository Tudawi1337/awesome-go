package main

// Soundpad — звуковая панель (soundboard) в духе Soundpad.
//
// Запуск:
//
//	./soundpad                 # http://127.0.0.1:8090
//	./soundpad -addr :8090     # слушать на всех интерфейсах
//	./soundpad -dir ~/Звуки    # свой каталог библиотеки
//
// Программа не требует зависимостей на этапе сборки: только стандартная
// библиотека Go. Для воспроизведения желателен ffmpeg (и/или ffplay), но
// приложение работает и без него — тогда звук играет сам браузер.

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const appVersion = "1.0.0"

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8090", "адрес прослушивания, например 127.0.0.1:8090 или :8090")
		dir     = flag.String("dir", "./sound-library", "каталог библиотеки звуков")
		open    = flag.Bool("open", true, "открыть интерфейс в браузере при запуске")
		out     = flag.String("out", "browser", "где играть звук: browser (в браузере) или server (ffplay на этой машине)")
		version = flag.Bool("version", false, "показать версию и выйти")
	)
	flag.Parse()

	if *version {
		fmt.Printf("soundpad %s\n", appVersion)
		return
	}
	if v := os.Getenv("PORT"); v != "" && !flagPassed("addr") {
		*addr = "0.0.0.0:" + v // среда песочницы/хостинги часто задают PORT
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		logger.Fatalf("не удалось определить путь к библиотеке: %v", err)
	}
	if err := ensureDir(absDir); err != nil {
		logger.Fatalf("не удалось создать каталог %s: %v", absDir, err)
	}

	p := newPlayer(logger)
	if *out == "server" {
		if err := p.setServerOutput(true); err != nil {
			logger.Printf("предупреждение: %v — остаюсь в режиме browser", err)
		}
	} else if *out != "browser" {
		logger.Fatalf("неизвестное значение -out=%q (ожидается browser или server)", *out)
	}
	st, err := newStore(absDir, p)
	if err != nil {
		logger.Fatalf("ошибка библиотеки звуков: %v", err)
	}

	srv := newServer(st, p, logger)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Fatalf("не удалось занять %s: %v", *addr, err)
	}

	banner(logger, ln, absDir, p)

	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("сервер остановлен: %v", err)
		}
	}()

	url := "http://" + displayHost(ln.Addr().String())
	if *open {
		openBrowser(url)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Println("останавливаюсь…")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	logger.Println("готово")
}

func flagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func banner(logger *log.Logger, ln net.Listener, dir string, p *player) {
	logger.Printf("Soundpad %s", appVersion)
	logger.Printf("интерфейс:  http://%s", displayHost(ln.Addr().String()))
	logger.Printf("библиотека: %s", dir)
	switch p.mode {
	case modeFFmpeg:
		logger.Printf("звук:       ffmpeg (%s) — поддержка mp3/ogg/flac/m4a и др.", p.ffmpegPath)
	case modeFFplay:
		logger.Printf("звук:       ffplay (%s) — без нормализации громкости, WAV/MP3 и др.", p.ffplayPath)
	default:
		logger.Printf("звук:       ffmpeg не найден — браузер будет играть только WAV-файлы.")
		logger.Printf("            поставьте FFmpeg (ffmpeg.org), чтобы получить полную поддержку форматов")
	}
}

// displayHost превращает адрес прослушивания в адрес для браузера.
func displayHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// openBrowser пытается открыть ссылку в браузере; ошибки игнорируются.
func openBrowser(url string) {
	if v := strings.TrimSpace(os.Getenv("SOUNDPAD_NO_BROWSER")); v != "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		// в контейнере/по ssh браузера может не быть — это нормально
		cmd.Process.Kill()
		return
	}
	go func() { _ = cmd.Wait() }()
}
