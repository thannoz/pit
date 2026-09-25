package inspect

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// shoot loads a page and takes the picture asked for.
func shoot(t *testing.T, body string, shot Shot) (*Screenshot, image.Image) {
	t.Helper()
	path := browser(t)
	srv := site(t, map[string]string{"/{$}": `<html><body style="margin:0">` + body + `</body></html>`})
	r, err := Capture(t.Context(), srv.URL+"/", Options{Browser: path, Screenshot: shot})
	if err != nil {
		t.Fatal(err)
	}
	if r.Screenshot == nil {
		t.Fatal("no picture")
	}
	img, err := png.Decode(bytes.NewReader(r.Screenshot.PNG))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != r.Screenshot.Width || b.Dy() != r.Screenshot.Height {
		t.Errorf("the picture is %dx%d, the report says %dx%d", b.Dx(), b.Dy(), r.Screenshot.Width, r.Screenshot.Height)
	}
	return r.Screenshot, img
}

var (
	red    = color.RGBA{255, 0, 0, 255}
	blue   = color.RGBA{0, 0, 255, 255}
	yellow = color.RGBA{255, 255, 0, 255}
)

func colourAt(t *testing.T, img image.Image, y int, want color.RGBA) {
	t.Helper()
	r, g, b, a := img.At(640, y).RGBA()
	if got := (color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}); got != want {
		t.Errorf("at y=%d the picture is %v, want %v", y, got, want)
	}
}

// A window, as tall as the screen, above a page that goes on below it.
const hero = `<div style="height:100vh;background:red"></div><div style="height:2000px;background:blue"></div>`

// TestScreenshotOfTheWindow is half the acceptance criterion for T-802:
// the picture shows what the window shows, and all of it.
func TestScreenshotOfTheWindow(t *testing.T) {
	shot, img := shoot(t, hero, Window)
	if shot.Width != Width || shot.Height != Height || shot.FullPage || shot.Cut != 0 {
		t.Errorf("shot = %dx%d full=%v cut=%d", shot.Width, shot.Height, shot.FullPage, shot.Cut)
	}
	// The window is filled to its bottom by what is 100vh tall: the
	// page has the window's size, not less.
	colourAt(t, img, 0, red)
	colourAt(t, img, Height-1, red)
}

// TestScreenshotOfTheWholePage is the other half: the picture goes on
// to the bottom of the page.
func TestScreenshotOfTheWholePage(t *testing.T) {
	shot, img := shoot(t, hero, FullPage)
	if shot.Width != Width || shot.Height != Height+2000 || !shot.FullPage || shot.Cut != 0 {
		t.Errorf("shot = %dx%d full=%v cut=%d", shot.Width, shot.Height, shot.FullPage, shot.Cut)
	}
	colourAt(t, img, 0, red)
	colourAt(t, img, Height-1, red)
	colourAt(t, img, Height, blue)
	colourAt(t, img, Height+1999, blue)
}

// A page shorter than the window is pictured as the window shows it.
func TestScreenshotOfAShortPage(t *testing.T) {
	shot, _ := shoot(t, `<p>Nothing here yet.</p>`, FullPage)
	if shot.Width != Width || shot.Height != Height {
		t.Errorf("shot = %dx%d", shot.Width, shot.Height)
	}
}

// A page taller than a picture can be is cut, and says so; what the
// picture shows up to there is still the page.
func TestScreenshotOfAVeryTallPage(t *testing.T) {
	var b strings.Builder
	for i := range 30 {
		fmt.Fprintf(&b, `<div style="height:1000px;background:%s"></div>`, map[bool]string{true: "blue", false: "yellow"}[i%2 == 0])
	}
	shot, img := shoot(t, b.String(), FullPage)
	if shot.Height != maxSide || shot.Cut != 30000 {
		t.Errorf("shot is %d tall, cut = %d", shot.Height, shot.Cut)
	}
	colourAt(t, img, 15999, yellow)
	colourAt(t, img, 16000, blue)
	colourAt(t, img, maxSide-1, blue)
}

// A page wider than a picture can be is cut at the side, too.
func TestScreenshotOfAVeryWidePage(t *testing.T) {
	shot, img := shoot(t, `<div style="width:20000px;height:100px;background:red"></div>`, FullPage)
	if shot.Width != maxSide || shot.Height != Height {
		t.Errorf("shot = %dx%d", shot.Width, shot.Height)
	}
	colourAt(t, img, 50, red)
}
