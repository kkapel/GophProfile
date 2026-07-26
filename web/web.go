// Package web содержит статические файлы веб-интерфейса.
package web

import "embed"

// StaticFS — вшитые в бинарник HTML-шаблоны веб-интерфейса.
//
//go:embed static
var StaticFS embed.FS
