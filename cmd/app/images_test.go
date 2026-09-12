package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractImageArgs(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "shot.PNG")
	if err := os.WriteFile(pngPath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("mixed args", func(t *testing.T) {
		rest, images, err := extractImageArgs([]string{"describe", "@" + pngPath, "@handle", "please"})
		if err != nil {
			t.Fatalf("extractImageArgs: %v", err)
		}
		if strings.Join(rest, " ") != "describe @handle please" {
			t.Errorf("rest = %v", rest)
		}
		if len(images) != 1 || images[0].MediaType != "image/png" ||
			images[0].Data != base64.StdEncoding.EncodeToString([]byte("png-bytes")) {
			t.Errorf("images = %#v", images)
		}
	})

	t.Run("missing file errors", func(t *testing.T) {
		if _, _, err := extractImageArgs([]string{"@" + filepath.Join(dir, "nope.png")}); err == nil {
			t.Fatal("expected error for missing image file")
		}
	})

	t.Run("no image args pass through", func(t *testing.T) {
		rest, images, err := extractImageArgs([]string{"just", "a", "query"})
		if err != nil || len(images) != 0 || len(rest) != 3 {
			t.Fatalf("rest=%v images=%v err=%v", rest, images, err)
		}
	})
}
