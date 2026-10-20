// Package render takes pictures of web pages in a headless Chrome (the
// renderer container, chromedp/headless-shell), over the DevTools protocol.
// The API uses it for question pictures (spec 9.4): the page comes whole,
// with its data inline, so the browser loads nothing from anywhere.
package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Width is a page's width in CSS pixels; pictures are drawn at Scale times
// that (2560 px, the most Telegram keeps).
const (
	Width = 1280
	Scale = 2
)

// MaxHeight bounds a picture (CSS pixels): Telegram takes a photo whose
// width and height add up to at most 10,000 px (2560 + 7200 here).
const MaxHeight = 3600

// ErrOff means no renderer is configured (RENDERER_URL).
var ErrOff = errors.New("question pictures aren't set up on the server")

// Browser is the renderer: the DevTools address of a headless Chrome, for
// example ws://renderer:9222 (a host name is resolved first, as Chrome
// wants). One picture at a time.
type Browser struct {
	URL     string
	Timeout time.Duration // per picture; 30 s if zero
	mu      sync.Mutex
}

// Size is what the page reports after drawing.
type Size struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Picture opens html in a new tab, calls fn(data) there (fn returns a
// promise of the picture's Size), and returns a PNG of the page's top-left
// Width × Size.Height, at Scale.
func (b *Browser) Picture(ctx context.Context, html, fn string, data any) ([]byte, error) {
	if b == nil || b.URL == "" {
		return nil, ErrOff
	}
	arg, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	timeout := b.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	actx, cancelAlloc := remote.NewAllocator(ctx, b.URL)
	defer cancelAlloc()
	tab, closeTab := chromedp.NewContext(actx)
	defer closeTab()

	setup := func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, t, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width: Width, Height: 720, DeviceScaleFactor: Scale,
		})
		if err != nil {
			return chromedp.Void{}, err
		}
		tree, err := cdp.Call(ctx, t, page.GetFrameTree, cdp.Empty{})
		if err != nil {
			return chromedp.Void{}, err
		}
		_, err = cdp.Call(ctx, t, page.SetDocumentContent, page.SetDocumentContentParams{FrameID: tree.FrameTree.Frame.ID, HTML: html})
		return chromedp.Void{}, err
	}
	if err := chromedp.Do(tab, chromedp.Navigate("about:blank"), setup); err != nil {
		return nil, fmt.Errorf("render: open the page: %w", err)
	}
	size, err := chromedp.Run(tab, chromedp.Evaluate[Size](fmt.Sprintf("%s(%s)", fn, arg), chromedp.EvalAwaitPromise))
	if err != nil {
		return nil, fmt.Errorf("render: draw: %w", err)
	}
	shot := func(ctx context.Context, t *chromedp.Target) ([]byte, error) {
		yes := true
		res, err := cdp.Call(ctx, t, page.CaptureScreenshot, page.CaptureScreenshotParams{
			Format:                page.CaptureScreenshotFormatPng,
			CaptureBeyondViewport: &yes,
			Clip:                  &page.Viewport{Width: Width, Height: min(max(size.Height, 1), MaxHeight), Scale: 1},
		})
		return res.Data, err
	}
	png, err := chromedp.Run(tab, shot)
	if err != nil {
		return nil, fmt.Errorf("render: picture: %w", err)
	}
	return png, nil
}
