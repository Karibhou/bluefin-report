package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	calls []string
	fail  map[string]bool // keyed by "gh sub" or "api gists|issues|comments|user"
	out   map[string]string
}

func (f *fake) exec(_ context.Context, _ []byte, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	k := args[0] + " " + args[1]
	if args[0] == "api" {
		p := args[1]
		if p == "--paginate" {
			p = args[2]
		}
		switch {
		case strings.Contains(p, "/comments"):
			k = "api comments"
		case strings.Contains(p, "/issues"):
			k = "api issues"
		default:
			k = "api " + strings.SplitN(p, "?", 2)[0]
		}
	}
	if f.fail[k] {
		return nil, []byte("boom"), errors.New("exit 1")
	}
	return []byte(f.out[k]), nil, nil
}

func (f *fake) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
		if strings.Contains(c, "--label") {
			panic("--label used")
		}
	}
	return n
}

func newFake() *fake {
	return &fake{fail: map[string]bool{}, out: map[string]string{
		"gist create":   "https://gist.github.com/abc\n",
		"issue create":  "Creating issue\nhttps://github.com/projectbluefin/dakota/issues/7\n",
		"issue comment": "https://github.com/projectbluefin/dakota/issues/3#c1\n",
		"api user":      "me\n",
	}}
}

var draft = Draft{Repo: "projectbluefin/dakota", Title: "t", Body: "b", Attachment: []byte("{}"), Queue: "3-human-queue"}

func TestRoute(t *testing.T) {
	for id, want := range map[string]string{
		"dakota": "projectbluefin/dakota", "dakota-gaming": "projectbluefin/dakota", "dakota-nvidia": "projectbluefin/dakota",
		"utah": "projectbluefin/utah", "utah-dx": "projectbluefin/utah",
		"bluefin": "", "bluefin-lts": "", "dakotax": "", "": "",
	} {
		if got := Route(id); got != want {
			t.Errorf("Route(%q)=%q, want %q", id, got, want)
		}
	}
	for _, repo := range []string{"evil/dakota", "projectbluefin/bluefin", "projectbluefin/bluefin-lts", "projectbluefin/common"} {
		f := newFake()
		if _, err := Submit(context.Background(), f.exec, t.TempDir(), Draft{Repo: repo}); err == nil || len(f.calls) != 0 {
			t.Errorf("%s not rejected before network", repo)
		}
	}
}

func TestSuccess(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID = NewReportID()
	r, err := Submit(context.Background(), f.exec, dir, d)
	if err != nil || r.GistURL != "https://gist.github.com/abc" || !strings.HasSuffix(r.IssueURL, "/issues/7") {
		t.Fatal(r, err)
	}
	if f.count("gist create") != 1 || f.count("issue create") != 1 {
		t.Fatal(f.calls)
	}
	if p, _ := Pending(dir); len(p) != 0 {
		t.Fatal("not Published", p)
	}
	s, err := load(filepath.Join(dir, d.ReportID, "state.json"))
	if err != nil || s.Draft.Attachment != nil || s.Draft.Body != "" || s.IssueURL == "" {
		t.Fatalf("published draft still holds the payload or lost its URL: %+v %v", s, err)
	}
	if r2, err := Submit(context.Background(), f.exec, dir, d); err != nil || r2 != r || f.count("issue create") != 1 {
		t.Fatal("re-submit of a published report must return the stored result", r2, err, f.calls)
	}
}

func TestGistFailureResumesWithoutDuplicate(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["gist create"] = true
	d := draft
	d.ReportID = NewReportID()
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("want error")
	}
	p, _ := Pending(dir)
	if f.count("issue") != 0 || len(p) != 1 || p[0].Stage != Uncertain {
		t.Fatal(f.calls, p)
	}
	// The gist was actually created before gh timed out; the retry must find it via REST, not upload again.
	f.fail["gist create"] = false
	f.out["api gists"] = "https://gist.github.com/abc\n"
	r, err := Submit(context.Background(), f.exec, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	if f.count("gist create") != 1 || f.count("issue create") != 1 || r.IssueURL == "" {
		t.Fatal(f.calls, r)
	}
	// Submitting a published report again is a no-op.
	if _, err := Submit(context.Background(), f.exec, dir, d); err != nil || f.count("issue create") != 1 {
		t.Fatal(err, f.calls)
	}
}

func TestSubmitRejectsChangedDraftOnRetry(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Draft)
	}{
		{"body", func(d *Draft) { d.Body = "freshly reviewed body" }},
		{"title", func(d *Draft) { d.Title = "freshly reviewed title" }},
		{"repo", func(d *Draft) { d.Repo = "projectbluefin/utah" }},
		{"mode", func(d *Draft) { d.Mode = Confirm }},
		{"issue reference", func(d *Draft) { d.IssueRef = "4" }},
		{"queue", func(d *Draft) { d.Queue = "another-queue" }},
		{"attachment", func(d *Draft) { d.Attachment = []byte(`{"reviewed":true}`) }},
		{"removed attachment", func(d *Draft) { d.Attachment = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, dir := newFake(), t.TempDir()
			f.fail["issue create"] = true
			d := draft
			d.ReportID, d.IssueRef = NewReportID(), "3"
			if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
				t.Fatal("want first issue write to fail")
			}
			path := filepath.Join(dir, d.ReportID, "state.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			calls := len(f.calls)
			remoteCalls := f.count("api") + f.count("gist") + f.count("issue")
			f.fail["issue create"] = false
			changed := d
			tt.change(&changed)
			if _, err := Submit(context.Background(), f.exec, dir, changed); err == nil {
				t.Fatal("want draft mismatch error")
			}
			if f.count("api")+f.count("gist")+f.count("issue") != remoteCalls {
				t.Fatal("changed retry must not look up or publish the saved payload", f.calls[calls:])
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("changed retry modified saved state", err)
			}
			// The unchanged consented draft can still resolve the uncertain write
			// and retry without recreating the already-uploaded attachment.
			r, err := Submit(context.Background(), f.exec, dir, d)
			if err != nil || r.IssueURL == "" || f.count("gist create") != 1 || f.count("issue create") != 2 {
				t.Fatal("unchanged retry failed", r, err, f.calls)
			}
		})
	}
}

func TestSubmitDistinguishesMissingAndEmptyAttachment(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID, d.Attachment = NewReportID(), []byte{}
	s := &Saved{ReportID: d.ReportID, Stage: Reviewed, Draft: d}
	if err := save(dir, s); err != nil {
		t.Fatal(err)
	}
	d.Attachment = nil
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("removing an empty attachment must require explicit review")
	}
	if f.count("api")+f.count("gist")+f.count("issue") != 0 {
		t.Fatal("attachment mismatch caused remote work", f.calls)
	}
}

func TestSubmitPreservesCorruptState(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID = NewReportID()
	reportDir := filepath.Join(dir, d.ReportID)
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(reportDir, "state.json")
	before := []byte(`{"ReportID":`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Submit(context.Background(), f.exec, dir, d)
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("want original corrupt-state error, got %v", err)
	}
	if f.count("api")+f.count("gist")+f.count("issue") != 0 {
		t.Fatal("corrupt state caused remote work", f.calls)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("corrupt state was overwritten", err)
	}
}

func TestSubmitPreservesUnreadableState(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID = NewReportID()
	path := filepath.Join(dir, d.ReportID, "state.json")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Submit(context.Background(), f.exec, dir, d)
	var pathError *os.PathError
	if !errors.As(err, &pathError) || pathError.Op != "read" {
		t.Fatalf("want original unreadable-state error, got %v", err)
	}
	if f.count("api")+f.count("gist")+f.count("issue") != 0 {
		t.Fatal("unreadable state caused remote work", f.calls)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatal("unreadable state was replaced", err)
	}
}

func TestUncertainResume(t *testing.T) {
	for _, found := range []bool{true, false} {
		f, dir := newFake(), t.TempDir()
		f.fail["issue create"] = true
		if _, err := Submit(context.Background(), f.exec, dir, draft); err == nil {
			t.Fatal("want error")
		}
		p, _ := Pending(dir)
		if len(p) != 1 || p[0].Stage != Uncertain || p[0].StartedAt.IsZero() {
			t.Fatal(p)
		}
		f.fail["issue create"] = false
		f.fail["search issues"] = true // search index lags; must not be used
		if found {
			f.out["api issues"] = "https://github.com/projectbluefin/dakota/issues/9\n"
		}
		r, err := Resume(context.Background(), f.exec, dir, p[0].ReportID)
		if err != nil {
			t.Fatal(err)
		}
		creates := f.count("issue create")
		if found && (creates != 1 || !strings.HasSuffix(r.IssueURL, "/9")) {
			t.Fatal("found: ", f.calls, r)
		}
		if !found && (creates != 2 || !strings.HasSuffix(r.IssueURL, "/7")) {
			t.Fatal("not found: ", f.calls, r)
		}
		if f.count("gist create") != 1 || f.count("api --paginate gists") != 0 || r.GistURL != "https://gist.github.com/abc" {
			t.Fatal(f.calls, r)
		}
		if f.count("api --paginate repos/projectbluefin/dakota/issues?creator=me&state=all&per_page=100&since=") != 1 {
			t.Fatal(f.calls)
		}
	}
}

func TestGistNotFoundRecreatedOnce(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["gist create"] = true
	Submit(context.Background(), f.exec, dir, draft)
	p, _ := Pending(dir)
	f.fail["gist create"] = false
	if _, err := Resume(context.Background(), f.exec, dir, p[0].ReportID); err != nil {
		t.Fatal(err)
	}
	if f.count("gist create") != 2 || f.count("issue create") != 1 || f.count("api --paginate repos") != 0 {
		t.Fatal(f.calls)
	}
}

func TestConfirm(t *testing.T) {
	f := newFake()
	r, err := Submit(context.Background(), f.exec, t.TempDir(), Draft{Mode: Confirm, Repo: "projectbluefin/dakota", Body: "same here", IssueRef: "#3"})
	if err != nil || f.count("gist") != 0 || f.count("issue comment --repo projectbluefin/dakota --body-file - -- 3") != 1 || f.count("issue create") != 0 || r.IssueURL == "" {
		t.Fatal(f.calls, r, err)
	}
}

func TestConfirmUncertainFoundComment(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["issue comment"] = true
	d := Draft{Mode: Confirm, Repo: "projectbluefin/dakota", Body: "x", IssueRef: "https://github.com/projectbluefin/dakota/issues/3"}
	Submit(context.Background(), f.exec, dir, d)
	p, _ := Pending(dir)
	f.out["api comments"] = "https://github.com/projectbluefin/dakota/issues/3#issuecomment-5\n"
	r, err := Resume(context.Background(), f.exec, dir, p[0].ReportID)
	if err != nil || f.count("issue comment") != 1 || !strings.HasSuffix(r.IssueURL, "-5") || f.count("api --paginate repos/projectbluefin/dakota/issues/3/comments?") != 1 {
		t.Fatal(err, f.calls, r)
	}
}

func TestNotSignedIn(t *testing.T) {
	f := newFake()
	f.fail["auth status"] = true
	if _, err := Submit(context.Background(), f.exec, t.TempDir(), draft); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal(err)
	}
}

func TestResumeNotSignedIn(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["gist create"] = true
	Submit(context.Background(), f.exec, dir, draft)
	p, _ := Pending(dir)
	f = newFake()
	f.fail["auth status"] = true
	if _, err := Resume(context.Background(), f.exec, dir, p[0].ReportID); !errors.Is(err, ErrNotSignedIn) || len(f.calls) != 1 {
		t.Fatal(err, f.calls)
	}
}

func TestResumeRejectsRepoOutsideAllowlist(t *testing.T) {
	dir := t.TempDir()
	if err := save(dir, &Saved{ReportID: "abc123", Stage: Reviewed, Draft: Draft{Repo: "projectbluefin/bluefin", Title: "t", Body: "b"}}); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	if _, err := Resume(context.Background(), f.exec, dir, "abc123"); err == nil || len(f.calls) != 0 {
		t.Fatal("old-routing draft resumed:", err, f.calls)
	}
}

func TestIssueRef(t *testing.T) {
	const repo = "projectbluefin/dakota"
	for _, ok := range []string{"12", "#12", "https://github.com/projectbluefin/dakota/issues/12", "https://github.com/ProjectBluefin/Dakota/issues/12#issuecomment-1"} {
		if n, err := ParseIssueRef(repo, ok); n != 12 || err != nil {
			t.Error(ok, n, err)
		}
	}
	for _, bad := range []string{"--web", "-1", "0", "#", "", "abc", "12 --web", "https://github.com/evil/repo/issues/12", "https://github.com/projectbluefin/utah/issues/12", "https://github.com/projectbluefin/dakota/pull/12"} {
		f := newFake()
		if _, err := Submit(context.Background(), f.exec, t.TempDir(), Draft{Mode: Confirm, Repo: repo, Body: "x", IssueRef: bad}); err == nil || len(f.calls) != 0 {
			t.Error(bad, err, f.calls)
		}
	}
}

func TestSignIn(t *testing.T) {
	var args []string
	gh := func(_ context.Context, _ []byte, on func(string), a ...string) error {
		args = a
		for _, l := range []string{
			"! First copy your one-time code: AB12-CD34",
			"Open this URL to continue in your web browser: https://github.com/login/device",
			"✓ Authentication complete.",
			"https://github.com/login/device",
		} {
			on(l)
		}
		return nil
	}
	n := 0
	err := SignIn(context.Background(), gh, func(code, url string) {
		n++
		if code != "AB12-CD34" || url != "https://github.com/login/device" {
			t.Fatal(code, url)
		}
	})
	if err != nil || n != 1 || strings.Join(args, " ") != "auth login --hostname github.com --git-protocol https --web --skip-ssh-key" {
		t.Fatal(err, n, args)
	}
}

func TestDiscard(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["gist create"] = true
	Submit(context.Background(), f.exec, dir, draft)
	p, _ := Pending(dir)
	if err := Discard(dir, p[0].ReportID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, p[0].ReportID)); !os.IsNotExist(err) || Discard(dir, "..") == nil {
		t.Fatal("not removed", err)
	}
}
