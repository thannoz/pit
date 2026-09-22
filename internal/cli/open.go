package cli

import (
	"context"
	"runtime"

	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/ui"
)

// openInBrowser shows a URL in whatever the system uses for one. A
// failure is worth mentioning but not worth failing over: the URL is
// on screen either way.
func openInBrowser(ctx context.Context, out *ui.Printer, url string) {
	name, args := browserCommand(url)
	if name == "" {
		out.Warnf("do not know how to open a browser on %s", runtime.GOOS)
		return
	}
	if _, err := (proc.Exec{}).Output(ctx, proc.Command{Name: name, Args: args}); err != nil {
		out.Warnf("could not open a browser: %v", err)
	}
}

func browserCommand(url string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{url}
	case "linux":
		return "xdg-open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "", nil
	}
}
