package publish

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fake struct {
	calls []string
	stdin []string
	fail  map[string]bool   // keyed by "gh sub" or "api gists|issues|comments|user"
	errs  map[string]string // stderr for a failing key; default "boom"
	out   map[string]string
}

const create = "api --method POST repos/projectbluefin/dakota/issues"

func (f *fake) exec(_ context.Context, in []byte, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	f.stdin = append(f.stdin, string(in))
	k := args[0] + " " + args[1]
	if args[1] == "artifact" {
		k = "artifact " + args[2]
	}
	if args[0] == "api" {
		p := args[1]
		if p == "--paginate" {
			p = args[2]
		}
		switch {
		case p == "--method":
			k = "issue create"
		case strings.Contains(p, "/comments"):
			k = "api comments"
		case strings.Contains(p, "/issues"):
			k = "api issues"
		default:
			k = "api " + strings.SplitN(p, "?", 2)[0]
		}
	}
	if f.fail[k] {
		if e, ok := f.errs[k]; ok {
			return nil, []byte(e), errors.New("exit 1")
		}
		return nil, []byte("boom"), errors.New("exit 1")
	}
	return []byte(f.out[k]), nil, nil
}

// in returns the stdin of the first call starting with prefix.
func (f *fake) in(prefix string) string {
	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return f.stdin[i]
		}
	}
	return ""
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
		"issue create":  "https://github.com/projectbluefin/dakota/issues/7 kind/bug,needs-triage\n",
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

const artifact = "issue artifact create 7 --body-file - --name systemd-report.json -R projectbluefin/dakota"

func TestSuccess(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID = NewReportID()
	r, err := Submit(context.Background(), f.exec, dir, d)
	if err != nil || r.GistURL != "" || !strings.HasSuffix(r.IssueURL, "/issues/7") {
		t.Fatal(r, err)
	}
	if f.count("gist") != 0 || f.count(create) != 1 || f.count("issue artifact list") != 0 || f.count(artifact) != 1 {
		t.Fatal(f.calls)
	}
	if in := f.in(artifact); in != "```json\n{}\n```\n" || !strings.HasPrefix(in, "```json\n") || !strings.HasSuffix(in, "\n```\n") {
		t.Fatalf("artifact is not fenced JSON: %q", in)
	}
	if b := f.in(create); !strings.Contains(b, "Full report: `systemd-report.json`, attached to this issue (or linked in a comment below)") ||
		!strings.Contains(b, "<!-- bluefin-report-id: "+d.ReportID+" -->") || !strings.Contains(b, "<!-- hive-queue: 3-human-queue -->") || strings.Contains(b, "gist") {
		t.Fatalf("issue body: %q", b)
	}
	if p, _ := Pending(dir); len(p) != 0 {
		t.Fatal("not Published", p)
	}
	s, err := load(filepath.Join(dir, d.ReportID, "state.json"))
	if err != nil || s.Draft.Attachment != nil || s.Draft.Body != "" || s.IssueURL == "" {
		t.Fatalf("published draft still holds the payload or lost its URL: %+v %v", s, err)
	}
	if r2, err := Submit(context.Background(), f.exec, dir, d); err != nil || r2 != r || f.count(create) != 1 || f.count(artifact) != 1 {
		t.Fatal("re-submit of a published report must return the stored result", r2, err, f.calls)
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
		if len(p) != 1 || p[0].Stage != Uncertain || p[0].StartedAt.IsZero() || p[0].Draft.Attachment == nil {
			t.Fatal(p)
		}
		f.fail["issue create"] = false
		f.fail["search issues"] = true // search index lags; must not be used
		if found {
			f.out["api issues"] = "https://github.com/projectbluefin/dakota/issues/9\n"
			f.out["issue view"] = "kind/bug\n"
		}
		r, err := Resume(context.Background(), f.exec, dir, p[0].ReportID)
		if err != nil {
			t.Fatal(err)
		}
		creates := f.count(create)
		if found && (creates != 1 || !strings.HasSuffix(r.IssueURL, "/9") || f.count("issue artifact create 9 ") != 1) {
			t.Fatal("found: ", f.calls, r)
		}
		if !found && (creates != 2 || !strings.HasSuffix(r.IssueURL, "/7") || f.count(artifact) != 1) {
			t.Fatal("not found: ", f.calls, r)
		}
		if f.count("gist") != 0 || f.count("api --paginate gists") != 0 || r.GistURL != "" {
			t.Fatal(f.calls, r)
		}
		if f.count("api --paginate repos/projectbluefin/dakota/issues?creator=me&state=all&per_page=100&since=") != 1 {
			t.Fatal(f.calls)
		}
	}
}

func TestResumeAfterCrashBeforeArtifact(t *testing.T) {
	for _, exists := range []bool{false, true} {
		f, dir := newFake(), t.TempDir()
		if err := save(dir, &Saved{ReportID: "r1", Stage: LabelsDone, Draft: draft, IssueURL: "https://github.com/projectbluefin/dakota/issues/7"}); err != nil {
			t.Fatal(err)
		}
		f.out["artifact list"] = "other.txt\n"
		if exists {
			f.out["artifact list"] = "other.txt\nsystemd-report.json\n"
		}
		if _, err := Resume(context.Background(), f.exec, dir, "r1"); err != nil {
			t.Fatal(err)
		}
		want := 1
		if exists {
			want = 0
		}
		if f.count(create) != 0 || f.count("issue artifact list 7 -R projectbluefin/dakota") != 1 || f.count(artifact) != want || f.count("gist") != 0 {
			t.Fatal(exists, f.calls)
		}
		if p, _ := Pending(dir); len(p) != 0 {
			t.Fatal(p)
		}
	}
}

func TestConfirm(t *testing.T) {
	f := newFake()
	r, err := Submit(context.Background(), f.exec, t.TempDir(), Draft{Mode: Confirm, Repo: "projectbluefin/dakota", Body: "same here", IssueRef: "#3"})
	if err != nil || f.count("gist") != 0 || f.count("issue artifact") != 0 || f.count("issue comment --repo projectbluefin/dakota --body-file - -- 3") != 1 || f.count(create) != 0 ||
		r.IssueURL != "https://github.com/projectbluefin/dakota/issues/3" || strings.Contains(f.in("issue comment"), "Full report") {
		t.Fatal(f.calls, r, err)
	}
}

func TestConfirmWithReport(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := Draft{ReportID: "abc", Mode: Confirm, Repo: "projectbluefin/dakota", Body: "Same problem here.", IssueRef: "3", Attachment: []byte("{}")}
	if _, err := Submit(context.Background(), f.exec, dir, d); err != nil {
		t.Fatal(err)
	}
	body := f.in("issue comment")
	if f.count("issue artifact create 3 --body-file - --name systemd-report-abc.json") != 1 || f.count("issue comment") != 1 || f.count("gist") != 0 ||
		!strings.Contains(body, "`systemd-report-abc.json`, attached to this issue") || !strings.Contains(body, "<!-- bluefin-report-id: abc -->") ||
		!strings.Contains(body, "<!-- bluefin-report-confirm -->") {
		t.Fatal(f.calls, body)
	}
	if p, _ := Pending(dir); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestConfirmArtifactRefused(t *testing.T) {
	f := newFake()
	f.fail["artifact create"] = true
	f.errs = map[string]string{"artifact create": "HTTP 403"}
	d := Draft{ReportID: "abc", Mode: Confirm, Repo: "projectbluefin/dakota", Body: "x", IssueRef: "3", Attachment: []byte("{}")}
	r, err := Submit(context.Background(), f.exec, t.TempDir(), d)
	if err != nil || r.GistURL != "https://gist.github.com/abc" || f.count("gist create --desc bluefin-report abc --filename systemd-report-abc.json -") != 1 ||
		f.count("issue comment") != 1 || !strings.Contains(f.in("issue comment"), "Full report: https://gist.github.com/abc") {
		t.Fatal(err, r, f.calls)
	}
}

func TestConfirmResumeAfterArtifact(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["issue comment"] = true // crash between artifact and comment
	d := Draft{ReportID: "abc", Mode: Confirm, Repo: "projectbluefin/dakota", Body: "x", IssueRef: "3", Attachment: []byte("{}")}
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("comment failure should be reported")
	}
	f.fail["issue comment"] = false
	f.out["artifact list"] = "systemd-report.json\nsystemd-report-abc.json\n"
	if _, err := Resume(context.Background(), f.exec, dir, "abc"); err != nil {
		t.Fatal(err)
	}
	// Second run started at CommentPending: no artifact listing or re-upload, the comment checked then posted once.
	if f.count("issue artifact create") != 1 || f.count("issue comment") != 2 || f.count("api --paginate repos/projectbluefin/dakota/issues/3/comments") != 1 {
		t.Fatal(f.calls)
	}
	// Crash before the artifact step finished: Resume lists by name and skips re-creating.
	f, dir = newFake(), t.TempDir()
	f.fail["artifact create"] = true
	Submit(context.Background(), f.exec, dir, d)
	f.fail["artifact create"] = false
	f.out["artifact list"] = "systemd-report-abc.json\n"
	if _, err := Resume(context.Background(), f.exec, dir, "abc"); err != nil || f.count("issue artifact create") != 1 || f.count("issue comment") != 1 {
		t.Fatal(err, f.calls)
	}
}

func TestConfirmUncertainFoundComment(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.fail["issue comment"] = true
	d := Draft{Mode: Confirm, Repo: "projectbluefin/dakota", Body: "x", IssueRef: "https://github.com/projectbluefin/dakota/issues/3"}
	Submit(context.Background(), f.exec, dir, d)
	p, _ := Pending(dir)
	f.out["api comments"] = "5\n"
	r, err := Resume(context.Background(), f.exec, dir, p[0].ReportID)
	if err != nil || f.count("issue comment") != 1 || !strings.HasSuffix(r.IssueURL, "/3") || f.count("api --paginate repos/projectbluefin/dakota/issues/3/comments?") != 1 {
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
	f.fail["issue create"] = true
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
	f.fail["issue create"] = true
	Submit(context.Background(), f.exec, dir, draft)
	p, _ := Pending(dir)
	if err := Discard(dir, p[0].ReportID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, p[0].ReportID)); !os.IsNotExist(err) || Discard(dir, "..") == nil {
		t.Fatal("not removed", err)
	}
}

func TestLabelsAndArtifact(t *testing.T) {
	for _, maintainer := range []bool{true, false} {
		f := newFake()
		f.out["issue create"] = "https://github.com/projectbluefin/dakota/issues/7 kind/bug,area/gaming\n"
		if !maintainer {
			f.out["issue create"] = "https://github.com/projectbluefin/dakota/issues/7 needs-triage\n"
		}
		d := draft
		d.Area = "gaming"
		if _, err := Submit(context.Background(), f.exec, t.TempDir(), d); err != nil {
			t.Fatal(err)
		}
		if f.count(create+" -f title=t -F body=@- -f labels[]=kind/bug -f labels[]=area/gaming -f type=Bug") != 1 {
			t.Fatal(f.calls)
		}
		slash := f.count("issue comment --repo projectbluefin/dakota --body-file - -- 7")
		if maintainer && slash != 0 || !maintainer && (slash != 1 || !slices.Contains(f.stdin, "/kind bug\n/area gaming")) {
			t.Fatal(maintainer, f.calls, f.stdin)
		}
		if f.count(artifact) != 1 {
			t.Fatal(f.calls)
		}
	}
}

func TestNoAttachmentNoArtifact(t *testing.T) {
	f := newFake()
	d := draft
	d.Attachment = nil
	if _, err := Submit(context.Background(), f.exec, t.TempDir(), d); err != nil || f.count("issue artifact") != 0 || f.count("gist") != 0 ||
		strings.Contains(f.in(create), "Full report") {
		t.Fatal(err, f.calls)
	}
}

func TestResumeAfterArtifactFailure(t *testing.T) {
	for _, exists := range []bool{false, true} {
		f, dir := newFake(), t.TempDir()
		f.out["issue create"] = "https://github.com/projectbluefin/dakota/issues/7 \n" // labels dropped
		f.fail["artifact create"] = true
		r, err := Submit(context.Background(), f.exec, dir, draft)
		p, _ := Pending(dir)
		if err == nil || r.IssueURL == "" || len(p) != 1 || p[0].Stage != LabelsDone {
			t.Fatal(err, r, p)
		}
		f.fail["artifact create"] = false
		f.out["issue comment"] = "" // comment already posted by us
		if exists {
			f.out["artifact list"] = "systemd-report.json\n"
		}
		if _, err := Resume(context.Background(), f.exec, dir, p[0].ReportID); err != nil {
			t.Fatal(err)
		}
		want := 2
		if exists {
			want = 1
		}
		if f.count(create) != 1 || f.count("issue comment") != 1 || f.count("issue artifact create") != want || f.count("gist") != 0 {
			t.Fatal(exists, f.calls)
		}
		if p, _ := Pending(dir); len(p) != 0 {
			t.Fatal(p)
		}
	}
}

func TestResumeAfterCrashPostIssue(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	// Crash right after create: state says IssueCreated, slash comment already on GitHub.
	if err := save(dir, &Saved{ReportID: "r1", Stage: IssueCreated, Draft: draft,
		IssueURL: "https://github.com/projectbluefin/dakota/issues/7"}); err != nil {
		t.Fatal(err)
	}
	f.out["issue view"] = "needs-triage\n"
	f.out["api comments"] = "123\n"
	if _, err := Resume(context.Background(), f.exec, dir, "r1"); err != nil {
		t.Fatal(err)
	}
	if f.count(create) != 0 || f.count("issue comment") != 0 || f.count(artifact) != 1 {
		t.Fatal(f.calls)
	}
}

func TestArtifactRefusedFallsBackToGist(t *testing.T) {
	for _, stderr := range []string{"HTTP 403: Resource not accessible by integration", `unknown command "artifact" for "gh issue"`} {
		f, dir := newFake(), t.TempDir()
		f.fail["artifact create"] = true
		f.errs = map[string]string{"artifact create": stderr}
		d := draft
		d.ReportID = NewReportID()
		r, err := Submit(context.Background(), f.exec, dir, d)
		if p, _ := Pending(dir); err != nil || r.IssueURL == "" || r.GistURL != "https://gist.github.com/abc" || len(p) != 0 {
			t.Fatalf("%q: err=%v r=%v pending=%v", stderr, err, r, p)
		}
		link := "Full report: https://gist.github.com/abc\n<!-- bluefin-report-id: " + d.ReportID + " -->"
		if f.count("gist create --desc bluefin-report "+d.ReportID+" --filename systemd-report.json -") != 1 || f.in("gist create") != "{}" ||
			f.count("issue comment --repo projectbluefin/dakota --body-file - -- 7") != 1 || f.in("issue comment") != link || f.count("api --paginate") != 0 {
			t.Fatal(stderr, f.calls, f.stdin)
		}
	}
	// A transient failure keeps it pending for Resume, without a gist.
	f, dir := newFake(), t.TempDir()
	f.fail["artifact create"] = true
	d := draft
	d.ReportID = NewReportID()
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("transient artifact failure should be reported")
	}
	if p, _ := Pending(dir); len(p) != 1 || p[0].Stage != LabelsDone || p[0].Draft.Attachment == nil || f.count("gist") != 0 {
		t.Fatal("transient artifact failure should stay pending", p, f.calls)
	}
}

func TestFallbackResumeNeverDuplicates(t *testing.T) {
	for _, posted := range []bool{false, true} {
		f, dir := newFake(), t.TempDir()
		f.fail["artifact create"], f.fail["gist create"] = true, true
		f.errs = map[string]string{"artifact create": "HTTP 403"}
		Submit(context.Background(), f.exec, dir, draft)
		p, _ := Pending(dir)
		if len(p) != 1 || p[0].Stage != Uncertain || p[0].IssueURL == "" {
			t.Fatal(p, f.calls)
		}
		// gh timed out after GitHub stored the gist: Resume finds it via REST instead of uploading again.
		f.fail["gist create"] = false
		f.out["api gists"] = "https://gist.github.com/abc\n"
		if posted {
			f.out["api comments"] = "55\n"
		}
		r, err := Resume(context.Background(), f.exec, dir, p[0].ReportID)
		if err != nil || r.GistURL != "https://gist.github.com/abc" {
			t.Fatal(err, r)
		}
		want := 1
		if posted {
			want = 0
		}
		if f.count(create) != 1 || f.count("gist create") != 1 || f.count("issue comment") != want || f.count("api --paginate repos/projectbluefin/dakota/issues/7/comments") != 1 {
			t.Fatal(posted, f.calls)
		}
		if p, _ := Pending(dir); len(p) != 0 {
			t.Fatal(p)
		}
	}
}

func shots(t *testing.T, n int, ext string, size int) []string {
	src := t.TempDir()
	var ps []string
	for i := range n {
		p := filepath.Join(src, strings.Repeat("s", i+1)+ext)
		var b bytes.Buffer // a real image: Submit re-encodes screenshots
		img := image.NewGray(image.Rect(0, 0, 1, 1))
		if ext == ".jpg" {
			jpeg.Encode(&b, img, nil)
		} else {
			png.Encode(&b, img)
		}
		if err := os.WriteFile(p, append(b.Bytes(), make([]byte, max(0, size-b.Len()))...), 0o644); err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
	}
	return ps
}

func TestScreenshots(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID = NewReportID()
	d.Screenshots = shots(t, 2, ".png", 10)
	if _, err := Submit(context.Background(), f.exec, dir, d); err != nil {
		t.Fatal(err)
	}
	if f.count("issue comment") != 1 {
		t.Fatal(f.calls)
	}
	c := f.calls[slices.IndexFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "issue comment") })]
	copied := filepath.Join(dir, d.ReportID, "screenshots")
	if c != "issue comment --repo projectbluefin/dakota --body-file - --attach "+filepath.Join(copied, "s.png")+" --attach "+filepath.Join(copied, "ss.png")+" -- 7" ||
		f.in("issue comment") != "Screenshots\n<!-- bluefin-report-screenshots: "+d.ReportID+" -->" {
		t.Fatalf("%q %q", c, f.in("issue comment"))
	}
	if _, err := os.Stat(copied); !os.IsNotExist(err) {
		t.Fatal("copied screenshots not removed when Published", err)
	}

	f = newFake()
	d.ReportID, d.Screenshots = NewReportID(), nil
	if _, err := Submit(context.Background(), f.exec, dir, d); err != nil || f.count("issue comment") != 0 {
		t.Fatal(err, f.calls)
	}
}

func TestScreenshotsResume(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID = NewReportID()
	d.Screenshots = shots(t, 1, ".gif", 10)
	f.fail["issue comment"] = true
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("want warning")
	}
	s, _ := load(filepath.Join(dir, d.ReportID, "state.json"))
	if s.Stage != ShotsPending {
		t.Fatal(s.Stage)
	}
	os.Remove(d.Screenshots[0]) // originals may move; the draft copy is used
	if fi, err := os.Stat(s.Draft.Screenshots[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("not copied 0600", err)
	}
	f = newFake()
	f.out["api comments"] = "123\n" // marker comment exists: the crashed call went through
	if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err != nil || f.count("issue comment") != 0 || f.count("api --paginate repos/projectbluefin/dakota/issues/7/comments") != 1 {
		t.Fatal(err, f.calls)
	}
	if p, _ := Pending(dir); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestScreenshotsConfirm(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := Draft{Mode: Confirm, Repo: "projectbluefin/dakota", Body: "b", IssueRef: "3", Screenshots: shots(t, 1, ".jpg", 10)}
	if _, err := Submit(context.Background(), f.exec, dir, d); err != nil {
		t.Fatal(err)
	}
	if f.count("issue comment") != 1 || !strings.Contains(f.calls[len(f.calls)-1], "--attach ") || !strings.HasSuffix(f.calls[len(f.calls)-1], "s.jpg -- 3") ||
		strings.Contains(f.in("issue comment"), "bluefin-report-screenshots") {
		t.Fatal(f.calls)
	}
}

func TestScreenshotLimits(t *testing.T) {
	for name, ps := range map[string][]string{
		"count": shots(t, 11, ".png", 1),
		"size":  shots(t, 1, ".png", MaxScreenshotBytes+1),
		"ext":   shots(t, 1, ".exe", 1),
	} {
		f := newFake()
		d := draft
		d.Screenshots = ps
		if _, err := Submit(context.Background(), f.exec, t.TempDir(), d); err == nil || len(f.calls) != 0 {
			t.Errorf("%s: err=%v calls=%v", name, err, f.calls)
		}
	}
}
