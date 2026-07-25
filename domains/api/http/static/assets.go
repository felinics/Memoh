package static

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var assetsFS embed.FS

func AssetsFS() fs.FS {
	return assetsFS
}

func WebFS() (fs.FS, error) {
	return fs.Sub(assetsFS, "web")
}
