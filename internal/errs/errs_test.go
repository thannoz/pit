package errs

import (
	"errors"
	"fmt"
	"testing"
)

var errSentinel = errors.New("the underlying cause")

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{"message only", New("docker is not running"), "docker is not running"},
		{"message and cause", Wrap(errSentinel, "cannot start db"), "cannot start db: the underlying cause"},
		{"cause only", &Error{Err: errSentinel}, "the underlying cause"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrapAndHintedPassNilThrough(t *testing.T) {
	// Callers wrap unconditionally; a nil error must stay nil rather
	// than becoming a non-nil error with an empty message.
	if got := Wrap(nil, "context"); got != nil {
		t.Errorf("Wrap(nil, ...) = %v, want nil", got)
	}
	if got := Hinted(nil, "hint"); got != nil {
		t.Errorf("Hinted(nil, ...) = %v, want nil", got)
	}
}

func TestUnwrapReachesTheCause(t *testing.T) {
	err := Wrap(fmt.Errorf("layer: %w", errSentinel), "outer")

	if !errors.Is(err, errSentinel) {
		t.Errorf("errors.Is did not find the sentinel through %v", err)
	}

	var target *Error
	if !errors.As(err, &target) {
		t.Error("errors.As did not find *Error")
	}
}

func TestHint(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "no hint anywhere",
			err:  Wrap(errSentinel, "outer"),
			want: "",
		},
		{
			name: "hint on the outermost error",
			err:  Wrap(errSentinel, "outer").WithHint("start docker"),
			want: "start docker",
		},
		{
			name: "hint further down the chain",
			err:  Wrap(New("inner").WithHint("install gh"), "outer"),
			want: "install gh",
		},
		{
			name: "outermost hint wins over a deeper one",
			err:  Wrap(New("inner").WithHint("deeper"), "outer").WithHint("outermost"),
			want: "outermost",
		},
		{
			name: "hint survives a plain fmt wrap",
			err:  fmt.Errorf("plain: %w", New("inner").WithHint("check the config")),
			want: "check the config",
		},
		{
			name: "hint attached without changing the message",
			err:  Hinted(errSentinel, "try again"),
			want: "try again",
		},
		{"nil error", nil, ""},
		{"foreign error", errSentinel, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Hint(tt.err); got != tt.want {
				t.Errorf("Hint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHintedKeepsTheMessage(t *testing.T) {
	err := Hinted(errSentinel, "try again")
	if got, want := err.Error(), errSentinel.Error(); got != want {
		t.Errorf("Error() = %q, want the original %q", got, want)
	}
}

func TestHintTerminatesOnACycleFreeChainWithoutHints(t *testing.T) {
	// Several nested *Error values, none with a hint: the walk has to
	// step past each one instead of looping on the first match.
	err := Wrap(Wrap(Wrap(errSentinel, "c"), "b"), "a")
	if got := Hint(err); got != "" {
		t.Errorf("Hint() = %q, want empty", got)
	}
}
