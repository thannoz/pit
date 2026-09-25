package inspect

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/thannoz/pit/internal/errs"
)

// MaxGIF is as large as a GIF pit makes gets: a comment's attachment,
// not a video.
const MaxGIF = 2 << 20

// frame is one picture of the page and when the browser painted it.
type frame struct {
	at   time.Time
	jpeg []byte
}

// screencast keeps the pictures of the last while of a page, as the
// browser paints them. It keeps them as the browser sends them, and
// decodes only those a GIF is made of.
type screencast struct {
	mu     sync.Mutex
	keep   time.Duration
	frames []frame
	ack    func(sessionID int64)
}

// startCast is startScreencast, a variable so that a test can have it
// fail.
var startCast = startScreencast

// startScreencast has the browser send a picture of the page whenever
// what it shows changes, scaled to fit width by height.
func startScreencast(ctx context.Context, keep time.Duration, width, height int64) (*screencast, error) {
	s := &screencast{keep: keep}
	s.ack = func(id int64) {
		// Unacknowledged frames make the browser stop sending more.
		go func() {
			_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				return page.ScreencastFrameAck(id).Do(ctx)
			}))
		}()
	}
	chromedp.ListenTarget(ctx, s.handle)
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.StartScreencast().WithFormat(page.ScreencastFormatJpeg).WithQuality(70).
			WithMaxWidth(width).WithMaxHeight(height).Do(ctx)
	}))
	return s, err
}

func (s *screencast) handle(ev any) {
	e, ok := ev.(*page.EventScreencastFrame)
	if !ok {
		return
	}
	s.ack(e.SessionID)
	data, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil {
		return
	}
	at := time.Now()
	if e.Metadata != nil && e.Metadata.Timestamp != nil {
		at = e.Metadata.Timestamp.Time()
	}
	s.add(frame{at: at, jpeg: data})
}

// add keeps a frame, and forgets what is older than the while kept --
// all but the last picture from before it, which is what the screen
// showed when the while began.
func (s *screencast) add(f frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	cut := f.at.Add(-s.keep)
	first := 0
	for first+1 < len(s.frames) && !s.frames[first+1].at.After(cut) {
		first++
	}
	s.frames = s.frames[first:]
}

// gif makes a GIF of the last while, up to until.
func (s *screencast) gif(until time.Time) ([]byte, error) {
	s.mu.Lock()
	frames := append([]frame(nil), s.frames...)
	s.mu.Unlock()
	return encodeGIF(frames, until.Add(-s.keep), until, MaxGIF)
}

// encodeGIF makes a GIF of frames between from and to, each shown until
// the next was painted, no larger than max bytes. It gives up detail
// before it gives up time: fewer pictures a second first, then smaller
// ones, because an error that happens in the last second has to be in
// it.
func encodeGIF(frames []frame, from, to time.Time, max int) ([]byte, error) {
	if len(frames) == 0 {
		return nil, errs.New("the browser sent no pictures to make a GIF of")
	}
	// A recording shorter than the while kept starts with its first
	// picture, not with seconds of it standing still.
	if frames[0].at.After(from) {
		from = frames[0].at
	}
	decoded := make([]*image.RGBA, len(frames))
	for i, f := range frames {
		img, err := jpeg.Decode(bytes.NewReader(f.jpeg))
		if err != nil {
			return nil, errs.Wrap(err, "a picture from the browser cannot be read")
		}
		rgba := image.NewRGBA(img.Bounds())
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
		decoded[i] = rgba
	}
	for _, try := range []struct {
		step  time.Duration
		scale float64
	}{
		{100 * time.Millisecond, 1}, {200 * time.Millisecond, 1}, {200 * time.Millisecond, 0.75},
		{300 * time.Millisecond, 0.6}, {500 * time.Millisecond, 0.5}, {time.Second, 0.4},
	} {
		data, err := encodeAt(frames, decoded, from, to, try.step, try.scale)
		if err != nil {
			return nil, err
		}
		if len(data) <= max {
			return data, nil
		}
	}
	return nil, errs.New("a GIF of the page does not fit in %d bytes", max)
}

// encodeAt samples the frames every step and draws them at scale. Each
// picture after the first holds only the part that changed: a page is
// mostly still, and what moves is what the GIF is for.
func encodeAt(frames []frame, decoded []*image.RGBA, from, to time.Time, step time.Duration, scale float64) ([]byte, error) {
	b := decoded[len(decoded)-1].Bounds()
	w, h := max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))
	out := &gif.GIF{Config: image.Config{Width: w, Height: h, ColorModel: color.Palette(palette.Plan9)}}

	var previous *image.Paletted
	lastShown := -1
	delay := int(step / (10 * time.Millisecond))
	for t := from; t.Before(to); t = t.Add(step) {
		// What the screen showed at t: the last picture painted by then.
		i := 0
		for i+1 < len(frames) && !frames[i+1].at.After(t) {
			i++
		}
		// The same picture again: shown longer, and not drawn again only
		// to find nothing changed.
		if i == lastShown {
			out.Delay[len(out.Delay)-1] += delay
			continue
		}
		lastShown = i
		current := quantize(decoded[i], w, h)
		img := current
		if previous != nil {
			changed := changedArea(previous, current)
			if changed.Empty() {
				out.Delay[len(out.Delay)-1] += delay
				continue
			}
			img = current.SubImage(changed).(*image.Paletted)
		}
		out.Image = append(out.Image, img)
		out.Delay = append(out.Delay, delay)
		out.Disposal = append(out.Disposal, gif.DisposalNone)
		previous = current
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil {
		return nil, errs.Wrap(err, "cannot write the GIF")
	}
	return buf.Bytes(), nil
}

// lut maps a colour, 5 bits to a channel, to its nearest in the Plan 9
// palette: finding the nearest of 256 colours for every pixel of every
// picture would take seconds.
var lut = sync.OnceValue(func() []uint8 {
	t := make([]uint8, 32*32*32)
	for r := range 32 {
		for g := range 32 {
			for b := range 32 {
				c := color.RGBA{uint8(r<<3 | r>>2), uint8(g<<3 | g>>2), uint8(b<<3 | b>>2), 255}
				t[r<<10|g<<5|b] = uint8(color.Palette(palette.Plan9).Index(c))
			}
		}
	}
	return t
})

// quantize draws src at w by h in the Plan 9 palette, each pixel from
// the nearest of src's. No dithering: the same picture always gives the
// same pixels, which is what finding the part that changed depends on,
// and a page's text stays crisp.
func quantize(src *image.RGBA, w, h int) *image.Paletted {
	dst := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
	t := lut()
	sb := src.Bounds()
	for y := range h {
		sy := sb.Min.Y + y*sb.Dy()/h
		for x := range w {
			sx := sb.Min.X + x*sb.Dx()/w
			o := src.PixOffset(sx, sy)
			p := src.Pix[o : o+3 : o+3]
			dst.Pix[y*dst.Stride+x] = t[int(p[0]>>3)<<10|int(p[1]>>3)<<5|int(p[2]>>3)]
		}
	}
	return dst
}

// changedArea is the smallest rectangle holding every pixel that
// differs between two pictures of the same size.
func changedArea(a, b *image.Paletted) image.Rectangle {
	bounds := a.Bounds()
	minX, minY, maxX, maxY := bounds.Max.X, bounds.Max.Y, -1, -1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		ao, bo := a.PixOffset(bounds.Min.X, y), b.PixOffset(bounds.Min.X, y)
		for x := range bounds.Dx() {
			if a.Pix[ao+x] != b.Pix[bo+x] {
				minX, maxX = min(minX, bounds.Min.X+x), max(maxX, bounds.Min.X+x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}
