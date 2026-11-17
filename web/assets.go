// Package web embeds public static assets and server-side templates.
package web

import "embed"

//go:embed templates/*.html static/*
var FS embed.FS
