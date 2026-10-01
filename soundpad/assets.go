package main

// assets.go — интерфейс вшивается в бинарник: приложение остаётся
// одним исполняемым файлом без внешних ресурсов.

import _ "embed"

//go:embed assets/index.html
var indexHTML string

//go:embed assets/app.js
var appJS string

//go:embed assets/app.css
var appCSS string
