package inspect

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func servePage(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body style="margin:0">` + body + `</body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// gifOf records a page for a while and returns its GIF, decoded.
func gifOf(t *testing.T, address string, keep, busy time.Duration) ([]byte, *gif.GIF) {
	t.Helper()
	path := browser(t)
	r, err := Record(t.Context(), address, RecordOptions{Browser: path, Headless: true, GIF: keep,
		Drive: func(ctx context.Context) error {
			return chromedp.Run(ctx, chromedp.WaitVisible("#item"), chromedp.Click("#item"),
				chromedp.SendKeys("#item", "Genmaicha"), chromedp.Sleep(busy))
		}})
	if err != nil {
		t.Fatal(err)
	}
	if r.GIFError != nil {
		t.Fatal(r.GIFError)
	}
	g, err := gif.DecodeAll(bytes.NewReader(r.GIF))
	if err != nil {
		t.Fatalf("not a GIF: %v", err)
	}
	return r.GIF, g
}

func length(g *gif.GIF) time.Duration {
	total := 0
	for _, d := range g.Delay {
		total += d
	}
	return time.Duration(total) * 10 * time.Millisecond
}

// TestAFiveSecondGIF is the acceptance criterion for T-809: the last
// five seconds of a recording become a GIF, and it stays under 2 MB.
func TestAFiveSecondGIF(t *testing.T) {
	address := servePage(t, `<input id="item" name="item"><div id="clock" style="font:40px sans-serif"></div>
<script>let n = 0; setInterval(() => { document.getElementById('clock').textContent = 'tick ' + (n++); }, 100);</script>`)
	data, g := gifOf(t, address, 5*time.Second, 6*time.Second)
	if n := len(data); n >= MaxGIF {
		t.Errorf("the GIF is %d bytes", n)
	}
	if d := length(g); d < 4800*time.Millisecond || d > 5200*time.Millisecond {
		t.Errorf("the GIF lasts %v", d)
	}
	if len(g.Image) < 20 {
		t.Errorf("a clock ticking ten times a second made %d pictures", len(g.Image))
	}
	// As wide as pit asks the browser for, and as tall as the page is
	// in the reviewer's window, which pit leaves as it is.
	if g.Config.Width != 960 || g.Config.Height < 400 || g.Config.Height > 600 {
		t.Errorf("the GIF is %dx%d", g.Config.Width, g.Config.Height)
	}
	t.Logf("%d bytes, %d pictures, %v", len(data), len(g.Image), length(g))
}

// A page that changes everywhere all the time is as bad as a GIF gets;
// it is made smaller until it fits, and still covers the whole while.
func TestAGIFOfABusyPageFits(t *testing.T) {
	address := servePage(t, `<input id="item"><canvas id="c" width="1280" height="800" style="display:block"></canvas>
<script>
const c = document.getElementById('c').getContext('2d');
const img = c.createImageData(1280, 800);
function draw() {
  for (let i = 0; i < img.data.length; i += 4) { const v = Math.random() * 255; img.data[i] = v; img.data[i+1] = 255 - v; img.data[i+2] = (v * 7) % 255; img.data[i+3] = 255; }
  c.putImageData(img, 0, 0);
  requestAnimationFrame(draw);
}
draw();
</script>`)
	data, g := gifOf(t, address, 5*time.Second, 6*time.Second)
	if len(data) > MaxGIF {
		t.Errorf("the GIF is %d bytes", len(data))
	}
	if d := length(g); d < 4500*time.Millisecond || d > 5500*time.Millisecond {
		t.Errorf("the GIF lasts %v", d)
	}
	t.Logf("%d bytes, %d pictures, %dx%d", len(data), len(g.Image), g.Config.Width, g.Config.Height)
}

func solid(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for x := 20; x < 60; x++ {
		for y := 20; y < 60; y++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A picture that did not change adds to how long the last one is
// shown; one that changed a little adds only the part that changed.
func TestGIFKeepsOnlyWhatChanged(t *testing.T) {
	start := time.Unix(1000, 0)
	red, blue := solid(t, color.RGBA{255, 0, 0, 255}), solid(t, color.RGBA{0, 0, 255, 255})
	frames := []frame{
		{at: start.Add(-time.Second), jpeg: red},
		{at: start.Add(500 * time.Millisecond), jpeg: red},
		{at: start.Add(time.Second), jpeg: blue},
	}
	data, err := encodeGIF(frames, start, start.Add(2*time.Second), MaxGIF)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 2 || g.Delay[0] != 100 || g.Delay[1] != 100 {
		t.Fatalf("%d pictures, delays %v", len(g.Image), g.Delay)
	}
	if b := g.Image[0].Bounds(); b != image.Rect(0, 0, 200, 100) {
		t.Errorf("the first picture is %v", b)
	}
	if b := g.Image[1].Bounds(); b.Dx() > 50 || b.Dy() > 50 || !b.In(image.Rect(10, 10, 70, 70)) {
		t.Errorf("the second picture is %v, more than the square that changed", b)
	}
}

// A recording shorter than the while kept makes a GIF as long as it.
func TestGIFOfAShortRecording(t *testing.T) {
	start := time.Unix(1000, 0)
	frames := []frame{
		{at: start.Add(7 * time.Second), jpeg: solid(t, color.RGBA{255, 0, 0, 255})},
		{at: start.Add(8 * time.Second), jpeg: solid(t, color.RGBA{0, 0, 255, 255})},
	}
	data, err := encodeGIF(frames, start, start.Add(10*time.Second), MaxGIF)
	if err != nil {
		t.Fatal(err)
	}
	g, _ := gif.DecodeAll(bytes.NewReader(data))
	if d := length(g); d != 3*time.Second {
		t.Errorf("the GIF lasts %v", d)
	}
}

func TestGIFThatCannotFit(t *testing.T) {
	start := time.Unix(1000, 0)
	frames := []frame{{at: start, jpeg: solid(t, color.RGBA{255, 0, 0, 255})}}
	if _, err := encodeGIF(frames, start, start.Add(time.Second), 10); err == nil || !strings.Contains(err.Error(), "does not fit in 10 bytes") {
		t.Errorf("err = %v", err)
	}
	if _, err := encodeGIF(nil, start, start.Add(time.Second), MaxGIF); err == nil {
		t.Error("a GIF of nothing")
	}
}

// Only the last while is kept, and the picture from just before it:
// what the screen showed when the while began.
func TestScreencastKeepsTheLastWhile(t *testing.T) {
	s := &screencast{keep: 5 * time.Second}
	start := time.Unix(1000, 0)
	for i := range 20 {
		s.add(frame{at: start.Add(time.Duration(i) * time.Second), jpeg: []byte{byte(i)}})
	}
	s.add(frame{at: start.Add(19500 * time.Millisecond), jpeg: []byte{20}})
	// The last is at 19.5 s; the while began at 14.5 s, when frame 14
	// was on the screen.
	if len(s.frames) != 7 || s.frames[0].jpeg[0] != 14 || s.frames[6].jpeg[0] != 20 {
		var kept []byte
		for _, f := range s.frames {
			kept = append(kept, f.jpeg[0])
		}
		t.Errorf("kept frames %v", kept)
	}
}

// The GIF is made as finely as fits: a smaller one is not taken when
// the first fits.
func TestGIFAsFineAsFits(t *testing.T) {
	start := time.Unix(1000, 0)
	var frames []frame
	for i := range 10 {
		frames = append(frames, frame{at: start.Add(time.Duration(i) * 100 * time.Millisecond), jpeg: solid(t, color.RGBA{uint8(i * 25), 0, 255, 255})})
	}
	decoded := make([]*image.RGBA, len(frames))
	for i, f := range frames {
		img, _ := jpeg.Decode(bytes.NewReader(f.jpeg))
		rgba := image.NewRGBA(img.Bounds())
		for y := range rgba.Bounds().Dy() {
			for x := range rgba.Bounds().Dx() {
				rgba.Set(x, y, img.At(x, y))
			}
		}
		decoded[i] = rgba
	}
	finest, err := encodeAt(frames, decoded, start, start.Add(time.Second), 100*time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeGIF(frames, start, start.Add(time.Second), len(finest))
	if err != nil || !bytes.Equal(got, finest) {
		t.Errorf("%v: %d bytes, the finest is %d", err, len(got), len(finest))
	}
}

// A recording whose pictures cannot be had still records the steps,
// and says why there is no GIF.
func TestRecordingWithoutAScreencast(t *testing.T) {
	path := browser(t)
	previous := startCast
	startCast = func(context.Context, time.Duration, int64, int64) (*screencast, error) {
		return nil, errors.New("screencasts are not supported")
	}
	t.Cleanup(func() { startCast = previous })
	address := servePage(t, `<input id="item">`)
	r, err := Record(t.Context(), address, RecordOptions{Browser: path, Headless: true, GIF: time.Second,
		Drive: func(ctx context.Context) error { return chromedp.Run(ctx, chromedp.SendKeys("#item", "x\r")) }})
	if err != nil {
		t.Fatal(err)
	}
	if r.GIF != nil || r.GIFError == nil || !strings.Contains(r.GIFError.Error(), "not supported") || len(r.Steps) < 2 {
		t.Errorf("recorded %+v", r)
	}
}
