// Package workspace deals with the git repository a review happens in:
// identifying it, and later checking pull requests out into isolated
// worktrees.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// LocalHost marks a repository whose remote is a filesystem path rather
// than a hosted service. Test repositories look like this, and so does
// a clone of a clone.
const LocalHost = "local"

// Identity names the repository a sandbox belongs to. Two clones of the
// same repository produce the same Identity no matter which URL form
// they were cloned with, which is what lets pit find its own state
// again.
type Identity struct {
	// Host is the service the repository lives on, lowercased, or
	// LocalHost for a filesystem remote.
	Host string
	// Owner is the account or group path. On GitLab it may contain
	// slashes, because subgroups nest.
	Owner string
	// Name is the repository itself, without the .git suffix.
	Name string
}

// String renders the canonical form, which is what the hash is taken
// over: "github.com/acme/shop".
func (i Identity) String() string {
	if i.Owner == "" {
		return i.Host + "/" + i.Name
	}
	return i.Host + "/" + i.Owner + "/" + i.Name
}

// Slug is a short, readable name safe to use in a directory or a Docker
// Compose project name. It is not unique on its own; pair it with Hash.
func (i Identity) Slug() string {
	// The innermost group is the useful one; a Windows directory's
	// separators are backslashes.
	owner := path.Base(strings.ReplaceAll(i.Owner, `\`, "/"))
	if owner == "." || owner == "/" {
		owner = ""
	}
	return sanitize(strings.Join(nonEmpty(owner, i.Name), "-"))
}

// Hash is a stable short digest of the canonical form. It disambiguates
// two repositories that share a Slug, and two clones of the same
// repository sitting in different directories.
func (i Identity) Hash() string {
	sum := sha256.Sum256([]byte(i.String()))
	return hex.EncodeToString(sum[:3])
}

// Ref is the Slug and Hash together: what pit uses for directory names
// and Compose project names.
func (i Identity) Ref() string { return i.Slug() + "-" + i.Hash() }

// scpLike matches git's abbreviated SSH syntax, which is not a URL and
// so has to be recognised before url.Parse gets a chance to mangle it:
// [user@]host:path
var scpLike = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/]+):(.+)$`)

// ParseRemoteURL turns any of git's remote URL forms into an Identity.
// Everything is lowercased: hosting services treat these names case
// insensitively, and pit needs one answer per repository, not two.
func ParseRemoteURL(raw string) (Identity, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Identity{}, errs.New("the remote has no URL").
			WithHint("check `git remote -v`")
	}

	if isLocalPath(raw) {
		return localIdentity(raw), nil
	}

	host, repoPath, err := split(raw)
	if err != nil {
		return Identity{}, err
	}

	owner, name := ownerAndName(repoPath)
	if name == "" {
		return Identity{}, errs.New("cannot tell which repository %q points at", raw).
			WithHint("expected a URL like https://github.com/owner/repo")
	}

	return Identity{
		Host:  strings.ToLower(host),
		Owner: strings.ToLower(owner),
		Name:  strings.ToLower(name),
	}, nil
}

// split separates the host from the repository path, handling both
// git's scp-like syntax and real URLs.
func split(raw string) (host, repoPath string, err error) {
	// A scheme means url.Parse understands it; without one, the
	// scp-like form is the only remaining possibility.
	if !strings.Contains(raw, "://") {
		m := scpLike.FindStringSubmatch(raw)
		if m == nil {
			return "", "", errs.New("cannot parse the remote URL %q", raw).
				WithHint("expected a URL like git@github.com:owner/repo.git")
		}
		return m[2], m[3], nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errs.Wrap(err, "cannot parse the remote URL %q", raw).
			WithHint("check `git remote -v`")
	}
	// u.Hostname() drops any user:password, which must never end up in
	// a directory name or a log line.
	return u.Hostname(), u.Path, nil
}

// ownerAndName splits a repository path into its owner part and the
// repository itself. GitLab subgroups nest, so everything before the
// last segment is the owner.
func ownerAndName(repoPath string) (owner, name string) {
	repoPath = strings.Trim(repoPath, "/")
	repoPath = strings.TrimSuffix(repoPath, ".git")

	segments := nonEmpty(strings.Split(repoPath, "/")...)
	if len(segments) == 0 {
		return "", ""
	}

	name = segments[len(segments)-1]
	owner = strings.Join(segments[:len(segments)-1], "/")
	return owner, name
}

// isLocalPath reports whether raw points at the filesystem rather than
// at a service. file:// is explicit; a leading slash or dot is git's
// shorthand for the same thing, and so, on Windows, are a drive letter
// and a share: C:\repos\shop, \\server\repos\shop.
func isLocalPath(raw string) bool {
	return strings.HasPrefix(raw, "file://") ||
		strings.HasPrefix(raw, "/") ||
		strings.HasPrefix(raw, `\`) ||
		strings.HasPrefix(raw, ".") ||
		strings.HasPrefix(raw, "~") ||
		driveLetter.MatchString(raw)
}

// driveLetter is a Windows path. Read as git's scp-like form it would
// be the host "c"; git on Windows reads it as a path, and so does pit
// everywhere, since no one names an SSH host with one letter.
var driveLetter = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// localIdentity names a filesystem remote after its directory. The full
// path goes into the hash, so two directories never collide.
func localIdentity(raw string) Identity {
	p := strings.TrimPrefix(raw, "file://")
	// One separator, whatever the system wrote.
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimSuffix(strings.TrimRight(p, "/"), ".git")

	return Identity{
		Host:  LocalHost,
		Owner: strings.ToLower(path.Dir(p)),
		Name:  strings.ToLower(path.Base(p)),
	}
}

// unsafeChars matches everything a Docker Compose project name and a
// directory name cannot both contain.
var unsafeChars = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitize reduces s to lowercase letters, digits and dashes, and makes
// sure it starts with a character Compose accepts.
func sanitize(s string) string {
	s = unsafeChars.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "repo"
	}
	if s[0] < 'a' || s[0] > 'z' {
		// Compose project names must begin with a letter or digit, and
		// a leading digit confuses some shells less than a dash does.
		if s[0] < '0' || s[0] > '9' {
			s = "r" + s
		}
	}
	return s
}

// nonEmpty drops empty strings, which is what makes repeated slashes
// and trailing separators harmless.
func nonEmpty(in ...string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
